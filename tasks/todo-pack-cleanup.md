# Pack-vs-Flow Fixes + Legacy Cleanup + Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make doctor work for pack-only projects, add `orq-lite pack install`, formalize cutover evidence as machine-readable, and clean stale docs (README/guide/CHANGELOG/CONTEXT/governed-pack).

**Architecture:** Doctor switches from the strict legacy 5-role resolution (`cfg.Resolve()`) to `cfg.ResolveAll()` plus a non-fatal warn listing missing legacy roles. A new `pack` CLI command wraps the existing `flow.LoadPack` digest verification and copies the pack into `.orquestalite/packs/<name>/<version>/`. Cutover evidence becomes a committed `benchmark/cutover-evidence.json` checked by the existing `orq-lite cutover check`. No legacy engine code is deleted (cutover gate is closed by design); the only "dead code" found on verification was a stale comment.

**Tech Stack:** Go (stdlib only — no new deps), `go test ./...`, existing `internal/flow` pack verification.

## Global Constraints

- No new module dependencies; stdlib + existing internal packages only.
- Every commit must pass `go build ./... && go vet ./... && go test ./...`.
- Do NOT delete or modify `internal/engine/`, `internal/loops/`, `internal/legacytest/`, `internal/cutover/` logic, `runcmd.go`, `factorycmd.go`, `aliases.go` — the cutover gate (tasks/todo.md milestone 6) blocks legacy deletion. Exception: the one stale comment in `internal/engine/builtins.go:13`.
- Do NOT edit `benchmark/round2/` or `benchmark/round3/` pack fixtures — they are frozen historical benchmark records.
- Commit messages: conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`), each ending with the line `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Branch: work on current branch `feat/bootstrap-and-governed-pack`.

**Verified findings this plan encodes (do not re-litigate):**
- `engine.WithActions` does not exist — only a stale comment at `internal/engine/builtins.go:13` mentions it.
- `loops.RunTaskLoop` has 17 test call sites; NOT worth removing (dies with the package at cutover).
- The `flows/` root scan in `listWorkflows` is LIVE, supported standalone-v2 layout (see `compileWorkflowTarget` at `internal/commands/workflowcmd.go:79` + `internal/flow/catalog.go:37`). Do not remove.

---

### Task 1: `config.MissingOrchestratedRoles` helper

**Files:**
- Modify: `internal/config/config.go` (after `ResolveRoles`, ~line 281)
- Test: `internal/config/config_test.go` (append; create the file with `package config` if it does not exist)

**Interfaces:**
- Produces: `func (c *Config) MissingOrchestratedRoles() []string` — returns legacy orchestrated roles (parser, coder, tester, critic, reviewer) not declared in `c.Roles`, in the order of the package-level `orchestratedRoles` slice. Task 2 consumes this.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go` (add `"reflect"` to imports if missing):

```go
func TestMissingOrchestratedRoles(t *testing.T) {
	c := &Config{Roles: map[string]Role{"coder": {}, "critic": {}}}
	got := c.MissingOrchestratedRoles()
	want := []string{"parser", "tester", "reviewer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingOrchestratedRoles() = %v, want %v", got, want)
	}
	full := &Config{Roles: map[string]Role{
		"parser": {}, "coder": {}, "tester": {}, "critic": {}, "reviewer": {},
	}}
	if got := full.MissingOrchestratedRoles(); len(got) != 0 {
		t.Fatalf("full legacy set: MissingOrchestratedRoles() = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestMissingOrchestratedRoles -v`
Expected: FAIL with "c.MissingOrchestratedRoles undefined"

- [ ] **Step 3: Write minimal implementation**

Add to `internal/config/config.go` right after the `ResolveRoles` method (~line 281):

```go
// MissingOrchestratedRoles returns the legacy orchestrated roles (parser,
// coder, tester, critic, reviewer) that are not declared in the config, in
// canonical order. Legacy commands (plan/run/factory) require all of them;
// v2 `flow run` only needs the roles its compiled IR references.
func (c *Config) MissingOrchestratedRoles() []string {
	var missing []string
	for _, name := range orchestratedRoles {
		if _, ok := c.Roles[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestMissingOrchestratedRoles -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add MissingOrchestratedRoles helper

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: doctor accepts pack-only projects (legacy roles become a warn)

**Files:**
- Modify: `internal/doctor/doctor.go:90-94`
- Test: `internal/doctor/doctor_test.go` (append)

**Interfaces:**
- Consumes: `cfg.ResolveAll()` (exists, `internal/config/config.go:244`) and `cfg.MissingOrchestratedRoles()` (Task 1).
- Produces: doctor check named `"legacy roles"` with `StatusWarn` when the legacy set is incomplete; `"team.json"` check passes for pack-only teams.

- [ ] **Step 1: Write the failing test**

Append to `internal/doctor/doctor_test.go`:

```go
func TestRun_PackOnlyTeamResolvesWithLegacyWarn(t *testing.T) {
	dir := t.TempDir()
	team := `{
  "agents": {"haiku": {"provider": "claude", "model": "claude-haiku-4-5-20251001"}},
  "roles": {
    "coder": {"agents": ["haiku"], "prompt": "prompts/coder.md", "result_path": ".orquestalite/results/coder.json", "timeout_seconds": 60}
  },
  "limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
  "rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2}
}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := Run(context.Background(), dir)
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if tc := byName["team.json"]; tc.Status != StatusOK {
		t.Fatalf("team.json = %+v, want StatusOK for a pack-only team", tc)
	}
	legacy, ok := byName["legacy roles"]
	if !ok || legacy.Status != StatusWarn {
		t.Fatalf("legacy roles = %+v, want StatusWarn", legacy)
	}
	for _, role := range []string{"parser", "tester", "reviewer"} {
		if !strings.Contains(legacy.Detail, role) {
			t.Fatalf("legacy roles detail %q missing %q", legacy.Detail, role)
		}
	}
}

func TestRun_FullLegacyTeamHasNoLegacyRolesWarn(t *testing.T) {
	dir := t.TempDir()
	role := func(name string) string {
		return `"` + name + `": {"agents": ["haiku"], "prompt": "prompts/` + name + `.md", "result_path": ".orquestalite/results/` + name + `.json", "timeout_seconds": 60}`
	}
	team := `{
  "agents": {"haiku": {"provider": "claude", "model": "claude-haiku-4-5-20251001"}},
  "roles": {` + role("parser") + `,` + role("coder") + `,` + role("tester") + `,` + role("critic") + `,` + role("reviewer") + `},
  "limits": {"max_review_cycles": 1, "max_fix_iterations": 1},
  "rate_limit_backoff": {"initial_seconds": 1, "factor": 2, "max_seconds": 2}
}`
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(team), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range Run(context.Background(), dir) {
		if c.Name == "legacy roles" {
			t.Fatalf("unexpected legacy roles check for a full legacy team: %+v", c)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/doctor/ -run 'TestRun_PackOnly|TestRun_FullLegacy' -v`
Expected: `TestRun_PackOnlyTeamResolvesWithLegacyWarn` FAILS (today `cfg.Resolve()` errors with `missing orchestrated role "parser"`, so team.json is StatusError and no "legacy roles" check exists). `TestRun_FullLegacyTeamHasNoLegacyRolesWarn` may pass already — that is fine, it pins the non-regression.

- [ ] **Step 3: Implement the doctor change**

In `internal/doctor/doctor.go`, replace lines 90–94:

```go
	if _, err := cfg.Resolve(); err != nil {
		add(StatusError, "team.json", "resolve: "+err.Error())
		return checks
	}
	add(StatusOK, "team.json", "loads and resolves")
```

with:

```go
	if _, err := cfg.ResolveAll(); err != nil {
		add(StatusError, "team.json", "resolve: "+err.Error())
		return checks
	}
	add(StatusOK, "team.json", "loads and resolves")
	if missing := cfg.MissingOrchestratedRoles(); len(missing) > 0 {
		add(StatusWarn, "legacy roles", "missing "+strings.Join(missing, ", ")+" — legacy commands (plan/run/factory) need them; v2 `flow run` does not")
	}
```

(`strings` is already imported.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/doctor/ -v`
Expected: ALL PASS (including pre-existing tests — `TestRun_EmptyDirReportsTeamJSONError` still passes because `config.Load` fails before resolution on a missing file).

- [ ] **Step 5: Run the full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages pass. If `internal/commands` doctor-related tests assert the old error behavior, update them to expect the warn (keep assertions strict, do not delete tests).

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "fix(doctor): pack-only projects pass; missing legacy roles downgrade to warn

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: remove legacy shim roles from governed-pack team.json

**Files:**
- Modify: `examples/governed-pack/team.json:12-14`
- Modify: `examples/governed-pack/README.md` (install section, lines ~44–69)

**Interfaces:**
- Consumes: Task 2 (doctor no longer errors without parser/tester/reviewer).
- Produces: a governed-pack `team.json` declaring only the 8 roles `factory-governed@1` actually uses.

- [ ] **Step 1: Edit team.json**

In `examples/governed-pack/team.json`, delete lines 12–14 (the `"parser"`, `"tester"`, `"reviewer"` role entries). The `"roles"` object then starts with `"ticket_planner"`. Ensure JSON stays valid (no leading comma).

- [ ] **Step 2: Update the README note**

In `examples/governed-pack/README.md`:
- In the "Run it" intro (~line 46–49), change "run `orq-lite init` first so the base `prompts/` (parser, tester, reviewer) exist, then install the pack and drop this team over the generated one" to: "run `orq-lite init` first to scaffold the project (`.gitignore`, base config), then install the pack and drop this team over the generated one".
- Delete the blockquote at lines ~65–69 ("> `orq-lite doctor` still imposes the legacy role set … unused by `factory-governed@1`.") and replace with:

```markdown
> `orq-lite doctor` reports a `legacy roles` **warn** for this team (no
> `parser`/`tester`/`reviewer`). That is expected and harmless: those roles are
> only needed by the legacy `plan`/`run`/`factory` commands, not by
> `orq-lite flow run`.
```

- [ ] **Step 3: Verify the team still resolves**

Run: `go test ./internal/config/ ./internal/doctor/ && python3 -c "import json; json.load(open('examples/governed-pack/team.json'))"`
Expected: tests pass; JSON parses.

- [ ] **Step 4: Commit**

```bash
git add examples/governed-pack/team.json examples/governed-pack/README.md
git commit -m "refactor(examples): drop legacy shim roles from governed-pack team

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `orq-lite pack install` command

**Files:**
- Create: `internal/commands/packcmd.go`
- Modify: `cmd/orq-lite/main.go` (new `case "pack":` next to `case "flow":` at ~line 291; plus one line in `usage()`)
- Test: `internal/commands/packcmd_test.go`

**Interfaces:**
- Consumes: `flow.LoadPack(root string) (*flow.Pack, error)` (`internal/flow/pack.go:62`) — full digest + unlisted-file + symlink verification; `pack.Files map[string]flow.Digest` with slash-separated, already-sanitized relative paths.
- Produces: `func PackCLI(ctx context.Context, projectDir string, args []string, out io.Writer) error` exported from `internal/commands`. Installs to `.orquestalite/packs/<name>/<version>/` (the first layout `compileWorkflowTarget` probes at `workflowcmd.go:60`).

- [ ] **Step 1: Write the failing tests**

Create `internal/commands/packcmd_test.go`:

```go
package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInstallablePack(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	flowBody := []byte(`{"apiVersion":"orq.dev/v2","kind":"Flow","metadata":{"name":"noop","version":"1"},"spec":{"inputs":{},"outputs":{},"steps":[]}}`)
	if err := os.MkdirAll(filepath.Join(src, "flows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "flows", "noop@1.json"), flowBody, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(flowBody)
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "orq.pack/v1",
		"name":       "development",
		"version":    "1",
		"files":      map[string]string{"flows/noop@1.json": hex.EncodeToString(sum[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pack.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestPackInstall_CopiesVerifiedPack(t *testing.T) {
	src := writeInstallablePack(t)
	project := t.TempDir()
	var out bytes.Buffer
	if err := PackCLI(context.Background(), project, []string{"install", src}, &out); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(project, ".orquestalite", "packs", "development", "1")
	for _, rel := range []string{"pack.json", filepath.Join("flows", "noop@1.json")} {
		if _, err := os.Stat(filepath.Join(installed, rel)); err != nil {
			t.Fatalf("missing installed file %s: %v", rel, err)
		}
	}
	if !strings.Contains(out.String(), "installed development@1") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPackInstall_RefusesOverwriteWithoutForce(t *testing.T) {
	src := writeInstallablePack(t)
	project := t.TempDir()
	if err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard); err != nil {
		t.Fatal(err)
	}
	err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected already-installed error mentioning --force, got %v", err)
	}
	if err := PackCLI(context.Background(), project, []string{"install", src, "--force"}, io.Discard); err != nil {
		t.Fatalf("force reinstall failed: %v", err)
	}
}

func TestPackInstall_RejectsTamperedPack(t *testing.T) {
	src := writeInstallablePack(t)
	if err := os.WriteFile(filepath.Join(src, "flows", "noop@1.json"), []byte(`{"tampered":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	err := PackCLI(context.Background(), project, []string{"install", src}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(project, ".orquestalite", "packs", "development")); !os.IsNotExist(statErr) {
		t.Fatalf("tampered pack must not leave files behind")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/commands/ -run TestPackInstall -v`
Expected: compile FAIL with "undefined: PackCLI"

- [ ] **Step 3: Implement packcmd.go**

Create `internal/commands/packcmd.go`:

```go
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/flow"
)

const packUsage = "usage: orq-lite pack install <pack-dir> [--force]"

// PackCLI implements `orq-lite pack <command>`. `install` verifies a pack
// directory against its pack.json manifest (every file digest, no unlisted
// files, no symlinks) and copies it to .orquestalite/packs/<name>/<version>/,
// the layout `orq-lite flow run <name>/<flow>@<version>` resolves.
func PackCLI(_ context.Context, projectDir string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("%s", packUsage)
	}
	force := false
	source := ""
	for _, arg := range args[1:] {
		switch {
		case arg == "--force" || arg == "-force":
			force = true
		case source == "":
			source = arg
		default:
			return fmt.Errorf("%s", packUsage)
		}
	}
	if source == "" {
		return fmt.Errorf("%s", packUsage)
	}
	return packInstall(projectDir, source, force, out)
}

func packInstall(projectDir, source string, force bool, out io.Writer) error {
	pack, err := flow.LoadPack(source)
	if err != nil {
		return err
	}
	dest := filepath.Join(projectDir, ".orquestalite", "packs", pack.Name, pack.Version)
	if _, statErr := os.Stat(dest); statErr == nil {
		if !force {
			return fmt.Errorf("pack %s@%s is already installed at %s (use --force to replace)", pack.Name, pack.Version, dest)
		}
		if err = os.RemoveAll(dest); err != nil {
			return err
		}
	}
	relatives := make([]string, 0, len(pack.Files)+1)
	relatives = append(relatives, "pack.json")
	for relative := range pack.Files {
		relatives = append(relatives, relative)
	}
	for _, relative := range relatives {
		if err = copyPackFile(source, dest, relative); err != nil {
			_ = os.RemoveAll(dest)
			return err
		}
	}
	// Re-verify the installed copy so a partial or racing write can never
	// leave an unverified pack behind.
	if _, err = flow.LoadPack(dest); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("installed copy failed verification: %w", err)
	}
	fmt.Fprintf(out, "installed %s@%s (%d files) at %s\n", pack.Name, pack.Version, len(pack.Files)+1, dest)
	fmt.Fprintf(out, "run flows with: orq-lite flow run %s/<flow>@%s\n", pack.Name, pack.Version)
	return nil
}

func copyPackFile(sourceRoot, destRoot, relative string) error {
	data, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(relative)))
	if err != nil {
		return err
	}
	target := filepath.Join(destRoot, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}
```

Note: `flow.LoadPack(source)` rejects tampered files before anything is copied, so `TestPackInstall_RejectsTamperedPack` passes with no dest writes at all.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/commands/ -run TestPackInstall -v`
Expected: 3/3 PASS

- [ ] **Step 5: Wire into main.go**

In `cmd/orq-lite/main.go`, immediately before `case "flow":` (~line 291), add:

```go
	case "pack":
		exit(commands.PackCLI(ctx, ".", args, os.Stdout))
```

Then locate `func usage()` in the same file and add this line to the command listing, formatted like its neighbors:

```
  pack install <dir>    verify a v2 pack and install it into .orquestalite/packs/
```

- [ ] **Step 6: End-to-end check with the real governed pack**

Run:
```bash
go build -o /tmp/orq-lite-dev ./cmd/orq-lite
cd "$(mktemp -d)" && git init -q
/tmp/orq-lite-dev pack install ~/Projects/personal/orquesta-lite/examples/governed-pack/pack
/tmp/orq-lite-dev flow validate development/factory-governed@1
cd -
```
Expected: `installed development@1 (…) at .orquestalite/packs/development/1`, and `flow validate` compiles the flow without errors.

- [ ] **Step 7: Full suite + commit**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS

```bash
git add internal/commands/packcmd.go internal/commands/packcmd_test.go cmd/orq-lite/main.go
git commit -m "feat(cli): add orq-lite pack install (verified copy into .orquestalite/packs)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: fix stale `WithActions` comment

**Files:**
- Modify: `internal/engine/builtins.go:12-13`

- [ ] **Step 1: Edit the comment**

Replace:

```go
// BuiltinActions returns the registry of native actions shipped with the
// engine. Callers may add more via WithActions.
```

with:

```go
// BuiltinActions returns the registry of native actions shipped with the
// engine.
```

- [ ] **Step 2: Build and commit**

Run: `go build ./internal/engine/`
Expected: builds.

```bash
git add internal/engine/builtins.go
git commit -m "docs(engine): drop reference to nonexistent WithActions

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: formalize cutover evidence as `benchmark/cutover-evidence.json`

**Files:**
- Create: `benchmark/cutover-evidence.json`
- Modify: `benchmark/README.md` (append a "Cutover evidence" section)

**Interfaces:**
- Consumes: `orq-lite cutover template` / `orq-lite cutover check` (`internal/commands/cutovercmd.go`), schema `internal/cutover/evidence.go` (`apiVersion: "orq.cutover/v1"`, `DisallowUnknownFields` — the JSON must contain EXACTLY the Evidence fields).
- Produces: a committed, machine-readable statement of current cutover-gate evidence. The gate is EXPECTED to stay CLOSED — the deliverable is a precise machine-readable gap list replacing "no evidence file exists".

- [ ] **Step 1: Generate the template**

```bash
go run ./cmd/orq-lite cutover template > benchmark/cutover-evidence.json
```

- [ ] **Step 2: Fill in what is factually true today**

Edit `benchmark/cutover-evidence.json`:
- `runtimeCommit`: output of `git rev-parse HEAD` at commit time (re-stamp in this step, do not reuse a stale sha).
- `parity`: keep all 15 template entries as-is (unfilled = honestly failing).
- `chaos`: keep the 4 template entries as-is.
- `benchmarks`: add one entry per completed benchmark round, with real data pulled from the reports. Read `benchmark/results/hybrid-final.md` (round 1), `benchmark/results/round2-r1.md` (round 2), and `benchmark/results/round3-r1.md` (round 3) and fill `id` (`round1-taskflow-hybrid`, `round2-hookrelay`, `round3-taskflow`), `model`, `completedAt` (RFC3339; use the report dates), `passed`/`criticalRegressions` per the report conclusions (round 3 shipped the delete-crash: `passed: false`, `criticalRegressions: 1`). Where a report does not state the git commit or a config digest, leave `commit`/`configDigest` as `""` — the check will flag them, which is the point. Do NOT invent values.
- `canary` / `rollback`: leave template zero-values (not yet performed).
- `controlledProjects`: `[{"name": "orquesta-lite", "path": ".."}]` (relative to `benchmark/`).
- `developmentPack`: `{"root": "../examples/governed-pack/pack", "name": "development", "version": "1", "manifestDigest": "PENDING"}`.

- [ ] **Step 3: Resolve the real manifest digest**

```bash
go run ./cmd/orq-lite cutover check --evidence benchmark/cutover-evidence.json --commit "$(git rev-parse HEAD)"
```

The `development-pack` gate FAILS with `installed pack identity mismatch: got development@1 digest=<sha256>`. Copy that digest into `manifestDigest` and re-run. The gate now fails only on the missing required flows (`factory-fast`, `issue-fix`, `plan-tickets`, `pr-review`, `task-list` do not exist in the governed pack) — that is the accurate current gap.

- [ ] **Step 4: Document in benchmark/README.md**

Append:

```markdown
## Cutover evidence

`cutover-evidence.json` is the machine-readable evidence document for the
legacy-runtime deletion gate (`orq-lite cutover check`). It records what has
actually been proven so far — benchmark rounds 1–3 — and leaves everything
unproven (parity scenarios, chaos runs, canary, rollback) honestly failing.

Check the current gate status with:

```sh
orq-lite cutover check --evidence benchmark/cutover-evidence.json --commit "$(git rev-parse HEAD)"
```

The gate is expected to be CLOSED; its output is the authoritative list of
what remains before the legacy engine can be deleted. Note the rounds 1–3
benchmark entries do not satisfy the benchmarks gate as-is: it requires ≥3
runs on the same commit, model, and config digest, and the recorded rounds
intentionally varied all three.
```

- [ ] **Step 5: Verify + commit**

Run the check once more and paste its full output into the commit message body (below the subject, above the trailer). Expected: `cutover gate: CLOSED` with per-gate details and exit code 1 — that exit code is correct behavior, not a task failure.

```bash
git add benchmark/cutover-evidence.json benchmark/README.md
git commit -m "chore(cutover): commit machine-readable evidence for the deletion gate

<paste of cutover check output>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: docs cleanup (README, guide, CHANGELOG, CONTEXT, governed-pack install)

**Files:**
- Modify: `README.md` (commands table ~200–219; v2 section ~385–406; Development ~444–451)
- Modify: `guide.md:65-66`, `guide.md:314`
- Modify: `CHANGELOG.md` (lines 14, 32; new Unreleased section)
- Modify: `CONTEXT.md:345-359`
- Modify: `examples/governed-pack/README.md` (install snippet ~53–55)

**Interfaces:**
- Consumes: `orq-lite pack install` (Task 4) — do this task after Task 4.

- [ ] **Step 1: README commands table**

In the `## Commands` block (~line 200), after the `orq-lite factory --replan` line, add:

```text
orq-lite pack install <dir>    verify a v2 pack and install it into .orquestalite/packs/
orq-lite flow run <ref> [k=v]  run a v2 flow, e.g. development/factory-governed@1 features_path=features.md
orq-lite flow list             list local v1/v2 flows
```

- [ ] **Step 2: README v2 example block (lines ~385–391)**

Replace:

```bash
orq-lite flow validate flows/release.json
orq-lite flow inspect development/release@1
orq-lite flow run development/release@1 issue=42 --policy=policy:safe@1
orq-lite flow status <run-id>
orq-lite flow resume <run-id>
```

with:

```bash
orq-lite pack install examples/governed-pack/pack
orq-lite flow validate development/factory-governed@1
orq-lite flow inspect development/factory-governed@1
orq-lite flow run development/factory-governed@1 features_path=features.md \
  --policy=.orquestalite/packs/development/1/policies/development@2.json
orq-lite flow status <run-id>
orq-lite flow resume <run-id>
```

- [ ] **Step 3: README legacy-default paragraph (lines ~393–397)**

Replace:

```
Historical development commands accept `--engine=legacy|v2`. Legacy remains
the default until the external development pack passes the documented parity,
benchmark, canary, and rollback gates; `--force-new-run` is required when an
unfinished legacy task/factory state exists. See
```

with:

```
`orq-lite flow run` (above) is the recommended path today and is not gated.
Separately, the historical commands (`plan`, `run`, `factory`, `review`,
`intake`) accept `--engine=legacy|v2`; legacy remains their default until the
development pack passes the documented parity, benchmark, canary, and rollback
gates, and `--force-new-run` is required when an unfinished legacy
task/factory state exists. See
```

- [ ] **Step 4: README Development section (lines ~444–451)**

- Line ~444: change "The repository's own `team.json`, `prompts/`, and `schemas/` at the repo root" to "The repository's own `team.json`, `prompts/`, `schemas/`, and `flows.json` at the repo root".
- Line ~449: change the comment `# scaffolds team.json / prompts/ / schemas/ from the embedded assets` to `# scaffolds team.json / prompts/ / schemas/ / flows.json from the embedded assets`.

- [ ] **Step 5: guide.md**

- Lines 65–66, replace:

```
Commit `team.json`, `prompts/`, and `flows.json` (project configuration);
`.orquestalite/` stays gitignored (runtime state).
```

with:

```
`init` adds `team.json`, `prompts/`, `schemas/`, and `flows.json` to
`.gitignore` on purpose: `run`'s rollback (`git clean -fd`) only removes
untracked files it can then regenerate, so keeping them untracked protects
them. Commit the `.gitignore` change; the config files themselves stay local.
```

- Line ~314, replace `- [ ] \`orq-lite init\`; config committed, \`.orquestalite/\` ignored` with `- [ ] \`orq-lite init\`; generated config present, \`.gitignore\` entries committed`.

- [ ] **Step 6: CHANGELOG.md**

- Replace both `factory-governed@4` occurrences (lines ~14 and ~32) with `factory-governed@1` and add a parenthetical on first occurrence: `(originally shipped as @4; renumbered to @1)`.
- Insert directly under the intro paragraph (before `## v0.2.3`):

```markdown
## Unreleased

### Added

- **`orq-lite pack install <dir>`** — verifies a pack against its `pack.json`
  manifest (digests, no unlisted files, no symlinks) and installs it to
  `.orquestalite/packs/<name>/<version>/`, replacing the manual `cp -R`.
- **`benchmark/cutover-evidence.json`** — machine-readable cutover-gate
  evidence; `orq-lite cutover check` output is now the authoritative gap list.

### Changed

- **`orq-lite doctor`** no longer fails pack-only projects: a `team.json`
  without the legacy `parser`/`tester`/`reviewer` roles now resolves, with a
  `legacy roles` warn noting that only `plan`/`run`/`factory` need them.
- **`examples/governed-pack/team.json`** dropped its unused legacy shim roles.
```

- [ ] **Step 7: CONTEXT.md CLI surface (lines ~345–359)**

- Delete the line `orq-lite run plan.md         # plan + run in one call (AFK mode)` (unimplemented — `run` takes no positional argument).
- Replace the bullet "`orq-lite run plan.md` with prior state asks for confirmation (or `--force`)." with "Planning and running are separate commands: `orq-lite plan plan.md` then `orq-lite run`."

- [ ] **Step 8: governed-pack README install snippet**

In `examples/governed-pack/README.md` (~lines 53–55), replace:

```sh
mkdir -p .orquestalite/packs/development
cp -R path/to/examples/governed-pack/pack .orquestalite/packs/development/1
```

with:

```sh
orq-lite pack install path/to/examples/governed-pack/pack
```

Also update the same pattern anywhere else it appears: `grep -rn "cp -R" README.md guide.md docs/ examples/` and fix every hit that installs a pack.

- [ ] **Step 9: Verify and commit**

Run: `grep -n "release@1\|factory-governed@4\|run plan.md" README.md CHANGELOG.md CONTEXT.md guide.md`
Expected: no hits (except, if any, inside historical benchmark notes — leave those).
Run: `go build ./... && go test ./...` (docs-only, but cheap insurance).

```bash
git add README.md guide.md CHANGELOG.md CONTEXT.md examples/governed-pack/README.md
git commit -m "docs: align README/guide/CHANGELOG/CONTEXT with governed pack + pack install

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Review

**Status: COMPLETE — 9 commits on `feat/bootstrap-and-governed-pack` (3e2c849..6667e2b), full suite green, final whole-branch review READY TO MERGE.**

All 7 tasks executed via subagent-driven development (fresh implementer + reviewer per task), plus one final-review fix wave:

- Task 1 `7563db6` — `config.MissingOrchestratedRoles()` helper (+test).
- Task 2 `c370476` — doctor: `ResolveAll()` + `legacy roles` warn; pack-only teams pass (+2 tests).
- Task 3 `43af234` — governed-pack team.json de-shimmed (8 roles); README note replaced. Collateral: `examples_test.go` Resolve→ResolveAll (guard restored later in 5b58feb).
- Task 4 `375c366` — `orq-lite pack install` (+3 tests, e2e vs the real governed pack, main.go + usage wiring).
- Task 5 `27733f1` — stale `WithActions` comment removed.
- Task 6 `0435b01` — `benchmark/cutover-evidence.json` (honest; gate CLOSED: 1/7 gates pass — legacy-runs; every filled value sourced to a report file) + benchmark/README section.
- Task 7 `f07c67c` — docs: README (fictional `release@1` → real refs, legacy-default paragraph split from `flow run`, commands table, flows.json in dogfooding list), guide.md gitignore correction, CHANGELOG Unreleased + @4→@1, CONTEXT.md AFK-mode removal, governed-pack `pack install` snippet.

**Deviations from plan (all reviewed):**
- Final review (fable) found 2 real bugs in the planned packcmd.go implementation: non-atomic install (SIGKILL → corrupt dest reported "already installed") and `--force` self-deletion when source is inside the install root. Fixed in `5b58feb`: temp-dir staging + inline per-file sha256 verify + atomic rename (replaces the second LoadPack), source-inside-install-root guard (+`TestPackInstall_RejectsSourceInsideInstallRoot`), doctor warn reworded to "BLOCKING for plan/run/factory", `critic` added to the doctor test assertion, legacy-example 5-role guard restored in examples_test.go, README `@2` policy-revision note.
- `6667e2b` — fail-fast already-installed check before staging (UX regression flagged by re-review).

**Verification:** `go build ./... && go vet ./... && go test ./...` green (35 packages); `TestPackInstall` 4/4; e2e `pack install` + `flow validate development/factory-governed@1` OK; `cutover check` exits 1 with `cutover gate: CLOSED` (expected, recorded in the commit body of 0435b01).

**Shipped as-is (triaged by final review):** nil-Roles case of MissingOrchestratedRoles untested (safe by Go map semantics); ctx unused in PackCLI (sync FS I/O); round2 evidence `passed=true`+`criticalRegressions=1` (fields independent in checks.go, gate outcome identical); round1 midnight-UTC completedAt padding (documented); CONTEXT.md 116-char line (spec wording).
