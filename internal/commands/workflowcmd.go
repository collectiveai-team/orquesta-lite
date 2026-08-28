package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
	"github.com/collectiveai-team/orquesta-lite/internal/activity/builtin"
	activityprocess "github.com/collectiveai-team/orquesta-lite/internal/activity/process"
	"github.com/collectiveai-team/orquesta-lite/internal/agenthealth"
	"github.com/collectiveai-team/orquesta-lite/internal/artifacts"
	"github.com/collectiveai-team/orquesta-lite/internal/config"
	"github.com/collectiveai-team/orquesta-lite/internal/contextopt"
	"github.com/collectiveai-team/orquesta-lite/internal/doctor"
	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
	"github.com/collectiveai-team/orquesta-lite/internal/fallback"
	"github.com/collectiveai-team/orquesta-lite/internal/flow"
	"github.com/collectiveai-team/orquesta-lite/internal/invoke"
	"github.com/collectiveai-team/orquesta-lite/internal/opencodeattach"
	"github.com/collectiveai-team/orquesta-lite/internal/runid"
	"github.com/collectiveai-team/orquesta-lite/internal/sessions"
	"github.com/collectiveai-team/orquesta-lite/internal/workflow"
)

const agentHealthThreshold = 2

func resolveLogFormat(format eventlog.Format, out *os.File) eventlog.Format {
	if format == eventlog.FormatHuman || format == eventlog.FormatVerbose {
		return format
	}
	if out != nil {
		if info, err := out.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return eventlog.FormatHuman
		}
	}
	return eventlog.FormatVerbose
}

func runStaticAgentPreflight(cfg *config.Config, tracker *agenthealth.Tracker, logger *eventlog.Logger, roles []string) {
	used := map[string]bool{}
	for _, roleName := range roles {
		role, ok := cfg.Roles[roleName]
		if !ok {
			continue
		}
		for _, name := range append(append([]string{}, role.Agents...), role.EscalationLadder...) {
			used[name] = true
		}
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		agent, ok := cfg.Agents[name]
		if !ok {
			continue
		}
		binary := agent.Provider
		if binary == "" && len(agent.Cmd) > 0 {
			binary = agent.Cmd[0]
		}
		if binary == "" {
			continue
		}
		if _, err := exec.LookPath(binary); err != nil {
			tracker.Skip(name, agenthealth.ReasonUnreachable)
			logger.Log(eventlog.Event{Type: "preflight_skipped_agent", Fields: map[string]any{"agent": name, "binary": binary, "reason": "binary_not_in_path"}})
			continue
		}
		if agent.Provider != "" && !doctor.ProviderHasUsableCredentials(agent.Provider) {
			tracker.Skip(name, agenthealth.ReasonNoCredentials)
			logger.Log(eventlog.Event{Type: "preflight_skipped_agent", Fields: map[string]any{"agent": name, "binary": binary, "provider": agent.Provider, "reason": "no_credentials"}})
		}
	}
}

type compiledWorkflow struct {
	Doc     *flow.Document
	IR      *flow.IR
	Catalog *flow.DirectoryCatalog
	Root    string
	Ref     string
}

func builtinSpecs() []activity.Spec {
	return []activity.Spec{(&builtin.AgentExecutor{}).Spec(), (&builtin.CommandExecutor{}).Spec(), (&builtin.GateExecutor{}).Spec(), (builtin.GateAssertExecutor{}).Spec(), (&builtin.ArtifactExecutor{}).Spec(), (builtin.ApprovalExecutor{}).Spec()}
}

func compileWorkflowTarget(projectDir, target string) (*compiledWorkflow, error) {
	var doc *flow.Document
	var catalog *flow.DirectoryCatalog
	var err error
	root := ""
	if info, statErr := os.Stat(target); statErr == nil && !info.IsDir() {
		doc, err = flow.Load(target)
		root = flow.PackRootForPath(target)
		catalog = flow.NewDirectoryCatalog(root, builtinSpecs())
	} else {
		if !strings.Contains(target, ":") && strings.Contains(target, "/") {
			resolvedRoot, flowRef, resolveErr := resolvePackFlowRef(projectDir, target)
			if resolveErr != nil {
				return nil, resolveErr
			}
			root, target = resolvedRoot, flowRef
		}
		ref, parseErr := flow.ParseResourceRef(target)
		if parseErr != nil || ref.Kind != "flow" {
			return nil, fmt.Errorf("workflow target must be a v2 JSON path or flow:name@version ref")
		}
		roots := []string{}
		if root != "" {
			roots = append(roots, root)
		}
		roots = append(roots, projectDir, filepath.Join(projectDir, ".orquestalite", "packs"))
		for _, candidate := range roots {
			candidateCatalog := flow.NewDirectoryCatalog(candidate, builtinSpecs())
			resolved, _, resolveErr := candidateCatalog.ResolveDocument(ref)
			if resolveErr == nil {
				doc = resolved
				catalog = candidateCatalog
				root = candidate
				break
			}
		}
		if doc == nil {
			return nil, fmt.Errorf("flow %s not found in installed local catalogs", target)
		}
	}
	if err != nil {
		return nil, err
	}
	var pack *flow.Pack
	if _, statErr := os.Stat(filepath.Join(root, "pack.json")); statErr == nil {
		if pack, err = flow.LoadPack(root); err != nil {
			return nil, err
		}
	}
	ir, diagnostics := flow.Compile(doc, catalog)
	if diagnostics.HasErrors() {
		raw, _ := json.MarshalIndent(diagnostics, "", "  ")
		return nil, fmt.Errorf("flow compilation failed:\n%s", raw)
	}
	if pack != nil {
		if err = flow.PinPack(ir, pack); err != nil {
			return nil, err
		}
	}
	return &compiledWorkflow{Doc: doc, IR: ir, Catalog: catalog, Root: root, Ref: "flow:" + ir.Metadata.Name + "@" + ir.Metadata.Version}, nil
}

type workflowDeps struct {
	runtime  *workflow.Runtime
	store    *workflow.Store
	logger   *eventlog.Logger
	registry *activity.Registry
}

func newWorkflowDeps(projectDir, teamPath, runID string, compiled *compiledWorkflow, format eventlog.Format) (*workflowDeps, error) {
	stateDir := filepath.Join(projectDir, ".orquestalite")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	store, err := workflow.Open(filepath.Join(stateDir, "workflows.db"))
	if err != nil {
		return nil, err
	}
	logger, err := eventlog.OpenWithFormat(filepath.Join(stateDir, "run.log"), os.Stdout, resolveLogFormat(format, os.Stdout))
	if err != nil {
		store.Close()
		return nil, err
	}
	if err = store.FlushOutbox(context.Background(), logger); err != nil {
		logger.Close()
		store.Close()
		return nil, fmt.Errorf("replay workflow outbox: %w", err)
	}
	logger.SetRunID(runID)
	registry := activity.NewRegistry()
	// Shell-capable executor; the durable scheduler enforces the per-run
	// AllowShell policy before dispatch, so capability never comes from flow
	// input alone.
	command := &builtin.CommandExecutor{DefaultDir: projectDir, AllowShell: true}
	gate := &builtin.GateExecutor{Command: command, ArtifactRoot: filepath.Join(stateDir, "runs", runID)}
	runtimeConfig := config.Runtime{}
	if raw, readErr := os.ReadFile(teamPath); readErr == nil {
		var partial struct {
			Runtime config.Runtime `json:"runtime"`
		}
		_ = json.Unmarshal(raw, &partial)
		runtimeConfig = partial.Runtime
	}
	artifactExecutor := &builtin.ArtifactExecutor{Root: filepath.Join(stateDir, "runs", runID), MaxBytes: runtimeConfig.ArtifactLimit(), AllowedReadRoots: []string{projectDir}}
	for _, executor := range []activity.Executor{command, gate, builtin.GateAssertExecutor{}, artifactExecutor, builtin.ApprovalExecutor{}} {
		if err = registry.Register(executor); err != nil {
			logger.Close()
			store.Close()
			return nil, err
		}
	}
	roles := referencedAgentRoles(compiled.IR)
	var invoker *invoke.RoleInvoker
	if len(roles) > 0 {
		cfg, loadErr := config.LoadDynamic(teamPath)
		if loadErr != nil {
			logger.Close()
			store.Close()
			return nil, loadErr
		}
		specs, resolveErr := cfg.ResolveRoles(roles)
		if resolveErr != nil {
			logger.Close()
			store.Close()
			return nil, resolveErr
		}
		tracker := agenthealth.New(agentHealthThreshold)
		runStaticAgentPreflight(cfg, tracker, logger, roles)
		backoff := cfg.Runtime.ProviderBackoff
		if backoff.InitialSeconds == 0 && backoff.MaxSeconds == 0 && backoff.Factor == 0 {
			backoff = cfg.RateLimitBackoff
		}
		fallbackCaller := fallback.NewCaller(fallback.Config{InitialBackoff: durationSeconds(backoff.InitialSeconds), Factor: backoff.Factor, MaxBackoff: durationSeconds(backoff.MaxSeconds)})
		artifactStore := &artifacts.Store{Root: filepath.Join(stateDir, "runs", runID)}
		resumeRoles := map[string]bool{}
		for _, role := range roles {
			resumeRoles[role] = cfg.Limits.SessionResumeEnabled()
		}
		defaultPattern := backoff.DefaultPattern
		if defaultPattern == "" {
			defaultPattern = cfg.RateLimitBackoff.DefaultPattern
		}
		// Resolve the context-optimization tools once per run and hand the
		// resulting environment to every agent invocation. Activate never
		// errors: an unavailable tool is reported and skipped, so a run is
		// never blocked by an optimization.
		optStatus := contextopt.Activate(cfg.Runtime.ContextOptimization, projectDir)
		for _, note := range optStatus.Notes {
			logger.Log(eventlog.Event{Type: "context_optimization", Fields: map[string]any{"detail": note}})
		}
		// Attach mode is verified here, before the run exists. The server
		// belongs to the user, so orq-lite cannot start one; the only honest
		// options on an unreachable server are to fail now or to silently run
		// detached, and the latter would leave team.json claiming a session
		// tree the run never built.
		attachManager, attachErr := newAttachManager(cfg.Attach, projectDir, runID, compiled.Ref)
		if attachErr != nil {
			logger.Close()
			store.Close()
			return nil, attachErr
		}
		invoker = &invoke.RoleInvoker{Specs: specs, Dir: projectDir, Fallback: fallbackCaller, Log: logger, Health: tracker, MemPath: filepath.Join(stateDir, "memory.md"), Runner: invoke.ExecRunner{}, DefaultRateLimitPattern: defaultPattern, AgentHealthThreshold: agentHealthThreshold, ConventionsPath: cfg.ConventionsFile, AgentEnv: optStatus.Env(), Sessions: sessions.Load(projectDir), ResumeRoles: resumeRoles, Artifacts: artifactStore, Attach: attachManager, CodeWritingRoles: map[string]bool{}}
	}
	validator := func(ref string, raw []byte) error {
		schema, ok := compiled.IR.Schemas[ref]
		if !ok {
			return fmt.Errorf("schema %s is not pinned", ref)
		}
		return schema.ValidateJSON(raw)
	}
	if err = registry.Register(&builtin.AgentExecutor{Invoker: invoker, Validate: validator}); err != nil {
		logger.Close()
		store.Close()
		return nil, err
	}
	if err = registerExternalActivities(compiled, registry, filepath.Join(stateDir, "runs", runID)); err != nil {
		logger.Close()
		store.Close()
		return nil, err
	}
	return &workflowDeps{runtime: &workflow.Runtime{Store: store, Activities: registry, Catalog: compiled.Catalog, Config: loadGateConfig(teamPath)}, store: store, logger: logger, registry: registry}, nil
}

func durationSeconds(value int) time.Duration { return time.Duration(value) * time.Second }

// newAttachManager builds the session-tree manager for a run, or returns nil
// when attach is not configured. Reachability is proven up front so a wedged or
// absent server fails at startup with a clear message instead of surfacing as a
// per-agent error partway through a long run.
func newAttachManager(attach config.Attach, projectDir, runID, flowRef string) (*opencodeattach.Manager, error) {
	if !attach.Enabled() {
		return nil, nil
	}
	client, err := opencodeattach.NewClient(attach.URL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opencodeattach.DefaultTimeout)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return nil, fmt.Errorf("attach.url is configured but not usable: %w (start one with `opencode serve`, or remove attach.url from team.json)", err)
	}
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	return opencodeattach.NewManager(client, absDir, runID, flowRef), nil
}

func registerExternalActivities(compiled *compiledWorkflow, registry *activity.Registry, artifactRoot string) error {
	seen := map[string]bool{}
	register := func(spec *activity.Spec, ref flow.ResourceRef) error {
		if spec == nil || seen[spec.Ref()] {
			return nil
		}
		seen[spec.Ref()] = true
		if _, ok := registry.Resolve(spec.Ref()); ok {
			return nil
		}
		candidates := []string{filepath.Join(compiled.Root, "activities", ref.Name+"@"+ref.Version+".json"), filepath.Join(compiled.Root, "activities", ref.Name+".json")}
		var raw []byte
		var err error
		for _, path := range candidates {
			raw, err = os.ReadFile(path)
			if err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("activity executable manifest %s: %w", ref, err)
		}
		manifest, err := activityprocess.DecodeManifest(raw)
		if err != nil {
			return err
		}
		executor := &activityprocess.Executor{Manifest: *manifest, Dir: compiled.Root, StderrRoot: artifactRoot}
		describeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		described, describeErr := executor.Describe(describeCtx)
		cancel()
		if describeErr != nil {
			return fmt.Errorf("describe external activity %s: %w", ref, describeErr)
		}
		if described.Ref() != manifest.Spec.Ref() || described.Effect != manifest.Spec.Effect || described.InputSchema != manifest.Spec.InputSchema || described.OutputSchema != manifest.Spec.OutputSchema {
			return fmt.Errorf("external activity %s describe contract differs from its pinned manifest", ref)
		}
		return registry.Register(executor)
	}
	var walk func(*flow.IR) error
	walk = func(ir *flow.IR) error {
		for _, step := range ir.Steps {
			if step.Subflow != nil {
				if err := walk(step.Subflow); err != nil {
					return err
				}
			}
			if err := register(step.Activity, step.Uses); err != nil {
				return err
			}
			for _, handler := range []*flow.IRHandler{step.OnError, step.OnCancel, step.Compensate} {
				if handler == nil {
					continue
				}
				if handler.Subflow != nil {
					if err := walk(handler.Subflow); err != nil {
						return err
					}
				}
				if err := register(handler.Activity, handler.Uses); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(compiled.IR)
}

func referencedAgentRoles(ir *flow.IR) []string {
	seen := map[string]bool{}
	inspect := func(spec *activity.Spec, with map[string]flow.Value) {
		if spec != nil && spec.Name == "agent.invoke" {
			if value, ok := with["role"]; ok {
				if role, ok := value.Literal.(string); ok {
					seen[role] = true
				}
			}
		}
	}
	var walk func(*flow.IR)
	walk = func(current *flow.IR) {
		for _, step := range current.Steps {
			if step.Subflow != nil {
				walk(step.Subflow)
			}
			inspect(step.Activity, step.With)
			for _, handler := range []*flow.IRHandler{step.OnError, step.OnCancel, step.Compensate} {
				if handler == nil {
					continue
				}
				inspect(handler.Activity, handler.With)
				if handler.Subflow != nil {
					walk(handler.Subflow)
				}
			}
		}
	}
	walk(ir)
	roles := make([]string, 0, len(seen))
	for role := range seen {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func (d *workflowDeps) close(ctx context.Context) error {
	flushErr := d.store.FlushOutbox(ctx, d.logger)
	logErr := d.logger.Close()
	storeErr := d.store.Close()
	return errors.Join(flushErr, logErr, storeErr)
}

// FlowCLI dispatches durable v2 workflow commands.
func FlowCLI(ctx context.Context, projectDir string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orq-lite flow <validate|inspect|list|run|status|resume|cancel|approve|events> <arguments>")
	}
	switch args[0] {
	case "validate", "inspect":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow %s <flow-ref-or-path>", args[0])
		}
		compiled, err := compileWorkflowTarget(projectDir, args[1])
		if err != nil {
			return err
		}
		if args[0] == "validate" {
			fmt.Fprintf(out, "valid %s@%s %s\n", compiled.IR.Metadata.Name, compiled.IR.Metadata.Version, compiled.IR.Digest)
			return nil
		}
		raw, _ := json.MarshalIndent(compiled.IR, "", "  ")
		_, err = fmt.Fprintln(out, string(raw))
		return err
	case "list":
		return listWorkflows(projectDir, out)
	case "run":
		return flowCLIRun(ctx, projectDir, args[1:], out)
	case "status", "events":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow %s <run-id>", args[0])
		}
		store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		if args[0] == "events" {
			events, err := store.ListEvents(ctx, args[1])
			if err != nil {
				return err
			}
			return writeJSONLines(out, events)
		}
		run, err := store.GetRun(ctx, args[1])
		if err != nil {
			return err
		}
		steps, err := store.ListSteps(ctx, args[1])
		if err != nil {
			return err
		}
		approvals, _ := store.ListApprovals(ctx, args[1])
		return writePrettyJSON(out, map[string]any{"run": run, "steps": steps, "approvals": approvals})
	case "resume":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow resume <run-id>")
		}
		return resumeWorkflow(ctx, projectDir, args[1], out)
	case "cancel":
		if len(args) != 2 {
			return fmt.Errorf("usage: orq-lite flow cancel <run-id>")
		}
		store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		return store.SetRunStatus(ctx, args[1], workflow.RunCancelled, "cancelled by operator")
	case "approve":
		if len(args) != 5 || args[3] != "--decision" {
			return fmt.Errorf("usage: orq-lite flow approve <run-id> <approval-id> --decision approve|reject")
		}
		store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
		if err != nil {
			return err
		}
		defer store.Close()
		return store.DecideApproval(ctx, args[1], args[2], args[4])
	default:
		return fmt.Errorf("unknown flow command %q", args[0])
	}
}

func flowCLIRun(ctx context.Context, projectDir string, args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: orq-lite flow run <flow-ref-or-path> [key=value ...]")
	}
	target := args[0]
	compiled, err := compileWorkflowTarget(projectDir, target)
	if err != nil {
		return err
	}
	sourceKey, policyTarget, inputArgs, err := splitRunOptions(args[1:])
	if err != nil {
		return err
	}
	inputs, err := workflowInputs(compiled.IR, inputArgs)
	if err != nil {
		return err
	}
	policy, err := loadWorkflowPolicy(compiled, policyTarget)
	if err != nil {
		return err
	}
	teamPath := filepath.Join(projectDir, "team.json")
	// Fail before the run exists, not twenty minutes into it: every config.*
	// reference must name a whitelisted key whose value is a non-empty argv.
	if err = validateConfigReferences(compiled.IR, loadGateConfig(teamPath)); err != nil {
		return err
	}
	id := runid.New()
	deps, err := newWorkflowDeps(projectDir, teamPath, id, compiled, eventlog.Format("auto"))
	if err != nil {
		return err
	}
	defer deps.close(context.Background())
	reportUnfinishedRun(ctx, deps.store, compiled.Ref, out)
	run, runErr := deps.runtime.Start(ctx, compiled.IR, workflow.StartOptions{RunID: id, SourceKey: sourceKey, FlowRef: compiled.Ref, Inputs: inputs, Policy: policy.Policy})
	if run != nil {
		policyRaw, _ := json.Marshal(policy.Policy)
		policyHash := sha256.Sum256(policyRaw)
		pack := "none"
		if compiled.IR.Pack != nil {
			pack = compiled.IR.Pack.Name + "@" + compiled.IR.Pack.Version + ":" + string(compiled.IR.Pack.Digest)
		}
		fmt.Fprintf(out, "run_id=%s status=%s flow=%s hash=%s policy=%s policy_source=%s policy_hash=%x pack=%s artifacts=%s\n", run.ID, run.Status, run.FlowRef, run.DefinitionHash, policy.Ref, policy.Source, policyHash, pack, filepath.Join(projectDir, ".orquestalite", "runs", run.ID))
	}
	return runErr
}

// reportUnfinishedRun makes an already-existing resume discoverable. It does
// not change semantics — the new run still starts — it just stops an operator
// (human or agent) from launching a duplicate without knowing one is in flight.
func reportUnfinishedRun(ctx context.Context, store *workflow.Store, flowRef string, out io.Writer) {
	existing, err := store.LatestUnfinishedRun(ctx, flowRef)
	if err != nil || existing == nil {
		return
	}
	fmt.Fprintf(out, "notice: %s already has an unfinished run %s (status=%s); resume it with: orq-lite flow resume %s\n", flowRef, existing.ID, existing.Status, existing.ID)
}

func splitRunOptions(args []string) (sourceKey, policyTarget string, inputs []string, err error) {
	sourceKey, remaining, err := splitSourceKey(args)
	if err != nil {
		return "", "", nil, err
	}
	inputs = make([]string, 0, len(remaining))
	for _, arg := range remaining {
		if strings.HasPrefix(arg, "--policy=") {
			if policyTarget != "" {
				return "", "", nil, fmt.Errorf("--policy may only be specified once")
			}
			policyTarget = strings.TrimPrefix(arg, "--policy=")
			if policyTarget == "" {
				return "", "", nil, fmt.Errorf("--policy cannot be empty")
			}
			continue
		}
		inputs = append(inputs, arg)
	}
	return sourceKey, policyTarget, inputs, nil
}

// resolvedPolicy records which precedence branch produced the run policy, so
// `flow run` can print it and an operator never has to infer which budget
// actually applied.
type resolvedPolicy struct {
	Policy workflow.Policy
	Source string // "flag", "flow-metadata", or "default"
	Ref    string
}

// loadWorkflowPolicy applies the policy precedence:
//
//  1. an explicit --policy=<ref|path>, which always wins;
//  2. the flow's own metadata.policy, already resolved and pinned into the IR
//     at compile time;
//  3. DefaultPolicy(), for ad-hoc flows that declare nothing.
//
// Branch 2 is the point of the whole thing: a pack that ships a policy used to
// have it ignored unless the operator named it by hand, which silently capped
// governed runs at the engine default's attempt budget.
func loadWorkflowPolicy(compiled *compiledWorkflow, target string) (resolvedPolicy, error) {
	if target != "" {
		policy, err := decodePolicyTarget(compiled, target)
		if err != nil {
			return resolvedPolicy{}, err
		}
		return resolvedPolicy{Policy: policy, Source: "flag", Ref: target}, nil
	}
	if declared := compiled.IR.Metadata.Policy; declared != "" {
		ref, err := flow.ParseResourceRef(declared)
		if err != nil || ref.Kind != "policy" {
			return resolvedPolicy{}, fmt.Errorf("invalid metadata.policy ref %q", declared)
		}
		raw, ok := compiled.IR.Policies[ref.String()]
		if !ok {
			return resolvedPolicy{}, fmt.Errorf("metadata.policy %s is not pinned in the compiled IR", declared)
		}
		policy, err := workflow.DecodePolicy(raw)
		if err != nil {
			return resolvedPolicy{}, err
		}
		return resolvedPolicy{Policy: policy, Source: "flow-metadata", Ref: declared}, nil
	}
	return resolvedPolicy{Policy: workflow.DefaultPolicy(), Source: "default", Ref: "default"}, nil
}

func decodePolicyTarget(compiled *compiledWorkflow, target string) (workflow.Policy, error) {
	var raw []byte
	if strings.HasPrefix(target, "policy:") {
		ref, err := flow.ParseResourceRef(target)
		if err != nil || ref.Kind != "policy" {
			return workflow.Policy{}, fmt.Errorf("invalid policy ref %q", target)
		}
		resolved, _, err := compiled.Catalog.ResolvePolicy(ref)
		if err != nil {
			return workflow.Policy{}, err
		}
		raw = resolved
	} else {
		var err error
		raw, err = os.ReadFile(target)
		if err != nil {
			return workflow.Policy{}, fmt.Errorf("read policy: %w", err)
		}
	}
	return workflow.DecodePolicy(raw)
}

func splitSourceKey(args []string) (string, []string, error) {
	var sourceKey string
	inputs := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "--source-key=") {
			if sourceKey != "" {
				return "", nil, fmt.Errorf("--source-key may only be specified once")
			}
			sourceKey = strings.TrimPrefix(arg, "--source-key=")
			if sourceKey == "" {
				return "", nil, fmt.Errorf("--source-key cannot be empty")
			}
			continue
		}
		inputs = append(inputs, arg)
	}
	return sourceKey, inputs, nil
}

func resumeWorkflow(ctx context.Context, projectDir, runID string, out io.Writer) error {
	store, err := workflow.Open(filepath.Join(projectDir, ".orquestalite", "workflows.db"))
	if err != nil {
		return err
	}
	run, err := store.GetRun(ctx, runID)
	store.Close()
	if err != nil {
		return err
	}
	var ir flow.IR
	if err = json.Unmarshal(run.IR, &ir); err != nil {
		return err
	}
	root := projectDir
	if ir.Pack != nil {
		root = findPinnedPack(projectDir, ir.Pack)
		if root == "" {
			return fmt.Errorf("pinned pack %s@%s is missing or its digest changed", ir.Pack.Name, ir.Pack.Version)
		}
	}
	compiled := &compiledWorkflow{IR: &ir, Catalog: flow.NewDirectoryCatalog(root, builtinSpecs()), Root: root}
	teamPath := filepath.Join(projectDir, "team.json")
	// A resume re-reads team.json live, so config.* must be re-validated here
	// too — the project may have changed since the run started.
	if err = validateConfigReferences(&ir, loadGateConfig(teamPath)); err != nil {
		return err
	}
	deps, err := newWorkflowDeps(projectDir, teamPath, runID, compiled, eventlog.Format("auto"))
	if err != nil {
		return err
	}
	defer deps.close(context.Background())
	updated, runErr := deps.runtime.Resume(ctx, runID)
	if updated != nil {
		fmt.Fprintf(out, "run_id=%s status=%s\n", updated.ID, updated.Status)
	}
	return runErr
}

func findPinnedPack(projectDir string, snapshot *flow.PackSnapshot) string {
	candidates := []string{projectDir, filepath.Join(projectDir, ".orquestalite", "packs", snapshot.Name, snapshot.Version), filepath.Join(projectDir, ".orquestalite", "packs", snapshot.Name+"@"+snapshot.Version)}
	for _, candidate := range candidates {
		if pack, err := flow.LoadPack(candidate); err == nil && snapshot.Matches(pack) {
			return candidate
		}
	}
	found := ""
	_ = filepath.WalkDir(projectDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Name() != "pack.json" {
			return nil
		}
		root := filepath.Dir(path)
		if pack, loadErr := flow.LoadPack(root); loadErr == nil && snapshot.Matches(pack) {
			found = root
		}
		return nil
	})
	return found
}

func workflowInputs(ir *flow.IR, args []string) (map[string]any, error) {
	inputs := map[string]any{}
	for name, spec := range ir.Inputs {
		if spec.Default != nil {
			value, err := flow.ResolveValue(*spec.Default, func(string) (any, bool) { return nil, false })
			if err != nil {
				return nil, err
			}
			inputs[name] = value
		}
	}
	for _, arg := range args {
		key, raw, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid input %q (expected key=value)", arg)
		}
		if _, declared := ir.Inputs[key]; !declared {
			return nil, fmt.Errorf("unknown input %q", key)
		}
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			value = raw
		}
		inputs[key] = value
	}
	return inputs, nil
}

func listWorkflows(projectDir string, out io.Writer) error {
	names := []string{}
	seen := map[string]bool{}
	roots := []string{filepath.Join(projectDir, "flows"), filepath.Join(projectDir, ".orquestalite", "packs")}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".json" || filepath.Base(filepath.Dir(path)) != "flows" {
				return nil
			}
			doc, loadErr := flow.Load(path)
			if loadErr != nil || doc.Kind != flow.KindFlow {
				return nil
			}
			name := "flow:" + doc.Metadata.Name + "@" + doc.Metadata.Version
			packRoot := flow.PackRootForPath(path)
			if pack, packErr := flow.LoadPack(packRoot); packErr == nil {
				name = pack.Name + "/" + doc.Metadata.Name + "@" + doc.Metadata.Version
			}
			if !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
			return nil
		})
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintln(out, name)
	}
	return nil
}
func writePrettyJSON(out io.Writer, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}
func writeJSONLines(out io.Writer, values []json.RawMessage) error {
	for _, raw := range values {
		if _, err := fmt.Fprintln(out, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
