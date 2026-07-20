package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/lionelchamorro/orquestalite/internal/commands"
	"github.com/lionelchamorro/orquestalite/internal/eventlog"
	"github.com/lionelchamorro/orquestalite/internal/web"
)

var version = "dev"

// defaultEngine stays legacy in normal builds until the cutover gate opens.
// Canary releases can dogfood v2 as the default without a source fork via:
// -ldflags "-X main.defaultEngine=v2".
var defaultEngine = "legacy"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	ctx := context.Background()
	commands.Version = version

	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		lang := fs.String("lang", "", "language hint: python|node|go|auto (default: autodetect)")
		precommit := fs.Bool("precommit", false, "write .pre-commit-config and set team.json lint_command for the detected language")
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		exit(commands.InitWithOptions(dir, commands.InitOptions{Lang: *lang, Precommit: *precommit}))

	case "plan":
		fs := flag.NewFlagSet("plan", flag.ExitOnError)
		appendFlag := fs.Bool("append", false, "append to existing tasks.json")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		_ = fs.Parse(args)
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: orq-lite plan <plan.md> [--append]")
			os.Exit(2)
		}
		selectedEngine, err := commands.SelectDevelopmentEngine(".", "plan", *engineName, flagWasSet(args, "engine"), false, os.Stderr)
		if err != nil {
			exit(err)
		}
		if selectedEngine == "v2" {
			exit(commands.RunDevelopmentAlias(ctx, ".", "plan", map[string]any{"plan_path": fs.Arg(0), "append": *appendFlag}, os.Stdout))
		}
		exit(commands.PlanWithLiveCaller(ctx, ".", fs.Arg(0), *appendFlag))

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		logFormat := fs.String("log-format", "auto", "stdout log format: auto|verbose|human")
		fast := fs.Bool("fast", false, "batch all pending tasks through coder once, then tester/critic once")
		serve := fs.Bool("serve", false, "also host the web dashboard while running")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address (with --serve)")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		forceNewRun := fs.Bool("force-new-run", false, "start v2 despite unfinished legacy tasks; preserves tasks.json")
		_ = fs.Parse(args)
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		startDashboard(runCtx, *serve, *addr)
		selectedEngine, err := commands.SelectDevelopmentEngine(".", "run", *engineName, flagWasSet(args, "engine"), *forceNewRun, os.Stderr)
		if err != nil {
			exit(err)
		}
		if selectedEngine == "v2" {
			if err := commands.CheckV2RunStart(".", *forceNewRun); err != nil {
				exit(err)
			}
			exit(commands.RunDevelopmentAlias(runCtx, ".", "run", map[string]any{"fast": *fast}, os.Stdout))
		}
		teamPath := "team.json"
		exit(commands.Run(runCtx, commands.RunOptions{
			ProjectDir: ".",
			TeamPath:   teamPath,
			LogFormat:  eventlog.Format(*logFormat),
			FastMode:   *fast,
		}))

	case "factory":
		fs := flag.NewFlagSet("factory", flag.ExitOnError)
		force := fs.Bool("force", false, "replace an existing unfinished queue")
		statusOnly := fs.Bool("status", false, "print the factory queue and exit")
		createPR := fs.Bool("pr", false, "push each finished feature branch and open a PR via gh")
		resume := fs.Bool("resume", false, "retry failed features (reuses their persisted task lists)")
		replan := fs.Bool("replan", false, "force fresh task decomposition for every feature (discards tasks-F*.json)")
		logFormat := fs.String("log-format", "auto", "stdout log format: auto|verbose|human")
		fast := fs.Bool("fast", false, "run coder/tester/critic once per feature, then final global review")
		serve := fs.Bool("serve", true, "host the web dashboard while running (on by default)")
		addr := fs.String("addr", "127.0.0.1:4173", "dashboard address")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		forceNewRun := fs.Bool("force-new-run", false, "start v2 despite unfinished legacy state; preserves legacy files")
		_ = fs.Parse(args)
		featuresPath := ""
		if fs.NArg() > 0 {
			featuresPath = fs.Arg(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		// The dashboard is on by default for factory runs so the operator
		// always sees its URL and can tell the run is live; --serve=false and
		// --status both suppress it.
		startDashboard(runCtx, *serve && !*statusOnly, *addr)
		selectedEngine, err := commands.SelectDevelopmentEngine(".", "factory", *engineName, flagWasSet(args, "engine"), *forceNewRun, os.Stderr)
		if err != nil {
			exit(err)
		}
		if selectedEngine == "v2" {
			if err := commands.CheckV2FactoryStart(".", *forceNewRun); err != nil {
				exit(err)
			}
			exit(commands.RunDevelopmentAlias(runCtx, ".", "factory", map[string]any{"features_path": featuresPath, "fast": *fast, "create_pr": *createPR}, os.Stdout))
		}
		exit(commands.Factory(runCtx, commands.FactoryOptions{
			ProjectDir:   ".",
			FeaturesPath: featuresPath,
			Force:        *force,
			StatusOnly:   *statusOnly,
			CreatePR:     *createPR,
			Resume:       *resume,
			Replan:       *replan,
			LogFormat:    eventlog.Format(*logFormat),
			FastMode:     *fast,
			Out:          os.Stdout,
		}))

	case "cost":
		exit(commands.Cost(ctx, ".", os.Stdout))

	case "precommit":
		fs := flag.NewFlagSet("precommit", flag.ExitOnError)
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		exit(commands.InitPrecommit(dir))

	case "review":
		fs := flag.NewFlagSet("review", flag.ExitOnError)
		pr := fs.String("pr", "", "PR number to review (resolved to base/head via gh)")
		base := fs.String("base", "", "explicit base git ref (overrides --pr resolution)")
		head := fs.String("head", "", "explicit head git ref (overrides --pr resolution)")
		publish := fs.Bool("publish", false, "post the verdict as a PR review via gh")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		_ = fs.Parse(args)
		if err := commands.ValidateEngine(*engineName); err != nil {
			exit(err)
		}
		if *engineName == "v2" {
			exit(commands.RunDevelopmentAlias(ctx, ".", "review", map[string]any{"pr": *pr, "base": *base, "head": *head, "publish": *publish}, os.Stdout))
		}
		exit(commands.Review(ctx, commands.ReviewOptions{
			ProjectDir: ".",
			PR:         *pr,
			Base:       *base,
			Head:       *head,
			Publish:    *publish,
		}))

	case "intake":
		fs := flag.NewFlagSet("intake", flag.ExitOnError)
		issue := fs.String("issue", "", "path to a file holding the GitHub issue body")
		noRun := fs.Bool("no-run", false, "stop after writing tasks.json (do not run the loop)")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		_ = fs.Parse(args)
		selectedEngine, err := commands.SelectDevelopmentEngine(".", "intake", *engineName, flagWasSet(args, "engine"), false, os.Stderr)
		if err != nil {
			exit(err)
		}
		if selectedEngine == "v2" {
			exit(commands.RunDevelopmentAlias(ctx, ".", "intake", map[string]any{"issue_path": *issue, "run": !*noRun}, os.Stdout))
		}
		exit(commands.Intake(ctx, commands.IntakeOptions{
			ProjectDir: ".",
			IssuePath:  *issue,
			Run:        !*noRun,
		}))

	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		interval := fs.Duration("interval", 60*time.Second, "poll interval")
		issues := fs.Bool("issues", false, "watch issues (trigger intake)")
		prs := fs.Bool("prs", false, "watch PRs (trigger review)")
		reviewOwn := fs.Bool("review-own-prs", false, "review PRs that orq-lite itself opened (default: skip)")
		publishPRs := fs.Bool("publish-prs", false, "post PR reviews via gh when reviewing")
		engineName := fs.String("engine", defaultEngine, "execution engine: legacy|v2")
		issueFlow := fs.String("issue-flow", "development/issue-fix@1", "v2 flow for issues")
		prFlow := fs.String("pr-flow", "development/pr-review@1", "v2 flow for pull requests")
		_ = fs.Parse(args)
		dir := "."
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		if err := commands.ValidateEngine(*engineName); err != nil {
			exit(err)
		}
		exit(commands.Watch(watchCtx, commands.WatchOptions{
			ProjectDir:   dir,
			Interval:     *interval,
			Issues:       *issues,
			PRs:          *prs,
			ReviewOwnPRs: *reviewOwn,
			PublishPRs:   *publishPRs,
			Out:          os.Stdout,
			Engine:       *engineName,
			IssueFlow:    *issueFlow,
			PRFlow:       *prFlow,
		}))

	case "doctor":
		exit(commands.Doctor(".", os.Stdout))

	case "cutover":
		exit(commands.CutoverCLI(ctx, ".", args, os.Stdout))

	case "index":
		fs := flag.NewFlagSet("index", flag.ExitOnError)
		rebuild := fs.Bool("rebuild", false, "delete orq.db and re-ingest the full log history")
		_ = fs.Parse(args)
		exit(commands.Index(".", *rebuild, os.Stdout))

	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		watch := fs.Bool("watch", false, "refresh status every interval until Ctrl+C")
		interval := fs.Duration("interval", time.Second, "watch refresh interval")
		_ = fs.Parse(args)
		if *watch {
			watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			exit(commands.StatusWatch(watchCtx, ".", os.Stdout, *interval))
		} else {
			exit(commands.Status(".", os.Stdout))
		}

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "127.0.0.1:4173", "listen address for the dashboard")
		_ = fs.Parse(args)
		serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		fmt.Printf("orquestalite dashboard: http://%s\n", *addr)
		exit(web.Serve(serveCtx, *addr, "."))

	case "log":
		fs := flag.NewFlagSet("log", flag.ExitOnError)
		role := fs.String("role", "", "show only agent_run events for this role")
		event := fs.String("event", "", "show only events of this type")
		expand := fs.Int("expand", 0, "print event #N in full (use the leading #N from the listing)")
		full := fs.Bool("full", false, "print full stdout/stderr/final_text tails for each agent_run")
		_ = fs.Parse(args)
		exit(commands.Log(".", os.Stdout, commands.LogViewOptions{
			Role:   *role,
			Event:  *event,
			Expand: *expand,
			Full:   *full,
		}))

	case "reset":
		exit(commands.Reset("."))

	case "update", "upgrade":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		check := fs.Bool("check", false, "report whether an update is available without installing")
		_ = fs.Parse(args)
		exit(commands.Update(ctx, commands.UpdateOptions{
			CurrentVersion: version,
			CheckOnly:      *check,
			Out:            os.Stdout,
		}))

	case "version", "--version", "-v":
		fmt.Println(version)

	case "flow":
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
			fmt.Fprintln(os.Stderr, `Usage: orq-lite flow <command>
  validate <ref|path>        compile and validate a v2 flow
  inspect <ref|path>         print pinned compiled IR
  list                       list local v1/v2 flows
  run <ref|path> [k=v...]    start a workflow (legacy names remain supported)
  status <run-id>            print run, steps and approvals
  resume <run-id>            resume the pinned workflow definition
  cancel <run-id>            cancel a workflow
  approve <run> <id> --decision approve|reject
  events <run-id>            print durable workflow events`)
			os.Exit(0)
		}
		runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		exit(commands.FlowCLI(runCtx, ".", args, os.Stdout))

	default:
		usage()
		os.Exit(2)
	}
}

// startDashboard hosts the web dashboard in the background while a run or
// factory command drives the loops in the foreground — the "one command"
// mode. It dies with the process; errors (e.g. port already in use) are
// reported but never abort the run.
func startDashboard(ctx context.Context, enabled bool, addr string) {
	if !enabled {
		return
	}
	fmt.Printf("orquestalite dashboard: http://%s\n", addr)
	go func() {
		if err := web.Serve(ctx, addr, "."); err != nil {
			fmt.Fprintln(os.Stderr, "dashboard:", err)
		}
	}()
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: orq-lite <command> [args]

Commands:
  init [--lang L] [dir] scaffold .orquestalite, team.json, prompts/ (--lang: python|node|go|auto; --precommit writes .pre-commit-config + lint_command)
  plan <plan.md>        invoke parser, write tasks.json (--engine legacy|v2)
  run [--log-format F]  run review/task/fix loops (--engine legacy|v2; --fast batches pending tasks)
  factory <features.md> develop each feature on its own branch (--engine legacy|v2; --force-new-run guards legacy state)
  precommit [dir]       write .pre-commit-config + set team.json lint_command for the detected language
  review [--pr N|--base B --head H] [--publish] critic-review a PR diff and post the verdict via gh
  intake --issue <file> triage a GitHub issue: plan+run, or write missing_info (--no-run)
  watch <project> [--issues] [--prs] [--interval D] poll GitHub; v2 emits idempotent generic flow triggers (--issue-flow, --pr-flow)
  cost                  per-task spend rollup (run.log sessions priced via agtop)
  doctor                preflight the setup (git, team.json, CLIs, credentials) before spending
  cutover check         verify the fail-closed legacy runtime deletion gate (--evidence path, --commit sha, --json)
  cutover template      print the versioned evidence document required by the deletion gate
  index [--rebuild]     build the sqlite read-model (.orquestalite/orq.db) from run.log
  status [--watch]      print tasks table (--watch refreshes until Ctrl+C)
  serve [--addr A]      web dashboard with live events (default 127.0.0.1:4173)
  log [--role R]        replay .orquestalite/run.log (--event T, --expand N, --full)
  reset                 remove .orquestalite state
  update [--check]      download and install the latest release from GitHub
  flow validate|inspect <ref|path> compile a strict v2 flow without executing it
  flow list             list legacy flows and locally installed versioned pack flows
  flow run <ref|path>   execute v2 flow data (--policy=<ref|path>, --source-key=<stable-key>, key=value...)
  flow status|events <run-id> inspect durable workflow state/history
  flow resume|cancel <run-id> resume the pinned IR or cancel a run
  flow approve <run-id> <approval-id> --decision approve|reject
  version               print the binary version`)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func flagWasSet(args []string, name string) bool {
	long := "--" + name
	short := "-" + name
	for _, arg := range args {
		if arg == long || arg == short || strings.HasPrefix(arg, long+"=") || strings.HasPrefix(arg, short+"=") {
			return true
		}
	}
	return false
}
