package checks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0644); e != nil {
		t.Fatal(e)
	}
}
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.test"}, {"config", "user.name", "test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	write(t, filepath.Join(dir, "source.txt"), "original\n")
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	return dir
}
func TestStrictProfileAndManifestDrift(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "package.json"), `{"scripts":{"test":"echo hello","e2e":"echo e2e"}}`)
	p, e := Detect(dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Items) != 2 {
		t.Fatal(p.Items)
	}
	b, _ := json.Marshal(p)
	if _, e = Decode(b); e != nil {
		t.Fatal(e)
	}
	for _, invalid := range []string{strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"mystery":true`, 1), strings.Replace(string(b), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), strings.Replace(string(b), `"required":true,`, "", 1)} {
		if _, e := Decode([]byte(invalid)); e == nil {
			t.Fatalf("accepted ambiguous profile %s", invalid)
		}
	}
	path := filepath.Join(dir, DefaultPath)
	write(t, path, string(b))
	r, e := Resolve(dir, "")
	if e != nil {
		t.Fatal(e)
	}
	if r.SourceHash != Hash(b) {
		t.Fatal("source hash mismatch")
	}
	write(t, filepath.Join(dir, "package.json"), `{"scripts":{"test":"different"}}`)
	if _, e = Resolve(dir, ""); e == nil {
		t.Fatal("accepted stale profile")
	}
}

func TestDetectMakeAndRustCommandsWithoutExecuting(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Makefile"), "test:\n\ttouch SHOULD_NOT_RUN\nlint:\n\ttrue\ntypecheck:\n\ttrue\nbuild:\n\ttrue\ne2e:\n\ttrue\n")
	write(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n")
	profile, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]Check{}
	for _, item := range profile.Items {
		items[item.ID] = item
	}
	if len(items) != 7 {
		t.Fatalf("missing manifest checks: %+v", items)
	}
	for _, target := range []string{"test", "lint", "typecheck", "build", "e2e"} {
		if !reflect.DeepEqual(items["make-"+target].Argv, []string{"make", "-C", ".", target}) {
			t.Fatalf("incorrect make proposal: %+v", items["make-"+target])
		}
	}
	if items["make-e2e"].Tier != 2 {
		t.Fatal("e2e proposal lost tier")
	}
	if !reflect.DeepEqual(items["unit-rust"].Argv, []string{"cargo", "test", "--manifest-path", "Cargo.toml"}) || !reflect.DeepEqual(items["build-rust"].Argv, []string{"cargo", "build", "--manifest-path", "Cargo.toml"}) {
		t.Fatalf("missing rust proposals: %+v", items)
	}
	if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_RUN")); !os.IsNotExist(err) {
		t.Fatal("detection executed Makefile recipe")
	}
}

func TestDetectDeclaredPackageManagerUsesNativeWorkingDirectory(t *testing.T) {
	for _, test := range []struct{ manager, flag string }{{"npm@11.0.0", "--prefix"}, {"pnpm@10.0.0", "--dir"}, {"yarn@4.0.0", "--cwd"}} {
		t.Run(test.manager, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "package.json"), `{"packageManager":"`+test.manager+`"}`)
			write(t, filepath.Join(dir, "pnpm-lock.yaml"), "lockfileVersion: 9\n")
			write(t, filepath.Join(dir, "apps", "web", "package.json"), `{"scripts":{"test":"touch SHOULD_NOT_RUN"}}`)
			profile, err := Detect(dir)
			if err != nil {
				t.Fatal(err)
			}
			manager, _, _ := strings.Cut(test.manager, "@")
			if len(profile.Items) != 1 || !reflect.DeepEqual(profile.Items[0].Argv, []string{manager, test.flag, "apps/web", "run", "test"}) {
				t.Fatalf("packageManager ignored: %+v", profile.Items)
			}
			if _, err := os.Stat(filepath.Join(dir, "SHOULD_NOT_RUN")); !os.IsNotExist(err) {
				t.Fatal("detection executed package script")
			}
		})
	}
}
func TestTierSelection(t *testing.T) {
	p := Profile{Items: []Check{{ID: "unit", Tier: 1}, {ID: "e2e", Tier: 3, Paths: []string{"apps/web/**"}}}}
	for _, tc := range []struct {
		s    Selection
		want int
	}{{Selection{Event: "pr", MaxTier: 1, DiffKnown: true, Paths: []string{"docs/a.md"}}, 1}, {Selection{Event: "pr", MaxTier: 1, DiffKnown: true, Paths: []string{"apps/web/a.ts"}}, 2}, {Selection{Event: "pr", MaxTier: 1, DiffKnown: false}, 2}, {Selection{Event: "release", DiffKnown: true}, 2}, {Selection{Event: "pr", MaxTier: 1, DiffKnown: true, Paths: []string{"package.json"}}, 2}} {
		selected, e := Select(p, tc.s)
		if e != nil || len(selected) != tc.want {
			t.Fatalf("%+v => %v %v", tc, selected, e)
		}
	}
	if !Match("apps/**/test?.go", "apps/test1.go") || !Match("apps/**/test?.go", "apps/web/tests/test2.go") {
		t.Fatal("recursive glob")
	}
}
func TestFreezeIsolatesDirtyAndUntrackedSources(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, "source.txt"), "dirty\n")
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	write(t, filepath.Join(dir, ".orquestalite", "run.log"), "excluded")
	before, e := git(context.Background(), dir, nil, "diff", "--cached")
	if e != nil {
		t.Fatal(e)
	}
	s, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	if !s.DiffKnown || s.ChangedLines != 3 || len(s.Paths) != 2 {
		t.Fatalf("bad diff %+v", s)
	}
	if e = VerifySnapshot(s); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.Dir, ".orquestalite")); !os.IsNotExist(e) {
		t.Fatal("runtime leaked into snapshot")
	}
	after, _ := git(context.Background(), dir, nil, "diff", "--cached")
	if string(before) != string(after) {
		t.Fatal("live index modified")
	}
	write(t, filepath.Join(dir, "source.txt"), "later\n")
	b, _ := os.ReadFile(filepath.Join(s.Dir, "source.txt"))
	if string(b) != "dirty\n" {
		t.Fatal("snapshot followed live source")
	}
	write(t, filepath.Join(s.Dir, "source.txt"), "tampered\n")
	if VerifySnapshot(s) == nil {
		t.Fatal("accepted tampered snapshot")
	}
}

func TestCurrentTreeMatchesSnapshotAndDetectsLaterChanges(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	write(t, filepath.Join(dir, DefaultPath), `{"approved":true}`)
	before, err := git(context.Background(), dir, nil, "diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := CurrentTree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if tree != snapshot.TreeHash {
		t.Fatal("live measurement differs from frozen source inclusion")
	}
	write(t, filepath.Join(dir, ".orquestalite", "runs", "ignored.log"), "runtime heartbeat")
	tree, err = CurrentTree(context.Background(), dir)
	if err != nil || tree != snapshot.TreeHash {
		t.Fatalf("runtime changed candidate hash: %v", err)
	}
	write(t, filepath.Join(dir, "source.txt"), "changed after approval\n")
	tree, err = CurrentTree(context.Background(), dir)
	if err != nil || tree == snapshot.TreeHash {
		t.Fatalf("later product change went undetected: %v", err)
	}
	after, err := git(context.Background(), dir, nil, "diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("CurrentTree changed the user's index")
	}
}
func TestExecutionEvidenceSilentFailureAndTamper(t *testing.T) {
	dir := repo(t)
	s, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	x := Execution{Root: root, RunID: "run", Scope: "step", Attempt: 1, ProfileHash: Hash([]byte("profile"))}
	c := Check{ID: "silent", Argv: []string{"sh", "-c", "test \"$(cat source.txt)\" = original"}, TimeoutSeconds: 2}
	evidence, e := Execute(context.Background(), c, s, x)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyEvidence(evidence.Receipt, s.TreeHash, x.ProfileHash); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(evidence.StdoutLog); e != nil {
		t.Fatal(e)
	}
	if _, e = Execute(context.Background(), c, s, x); e == nil {
		t.Fatal("overwrote immutable attempt")
	}
	if e = VerifyEvidence(evidence.Receipt, "wrong", x.ProfileHash); e == nil {
		t.Fatal("accepted wrong tree")
	}
	write(t, evidence.StdoutLog, "tampered")
	if VerifyEvidence(evidence.Receipt, s.TreeHash, x.ProfileHash) == nil {
		t.Fatal("accepted altered log")
	}
	c.ID = "failure"
	c.Argv = []string{"sh", "-c", "echo failure >&2; exit 7"}
	failure, e := Execute(context.Background(), c, s, x)
	if e != nil {
		t.Fatal(e)
	}
	if failure.ExitCode != 7 || !strings.Contains(failure.Stderr, "failure") {
		t.Fatalf("%+v", failure)
	}
	if VerifyEvidence(failure.Receipt, s.TreeHash, x.ProfileHash) == nil {
		t.Fatal("accepted failure")
	}
	c.ID = "mutation"
	c.Argv = []string{"sh", "-c", "echo changed > source.txt"}
	mutation, e := Execute(context.Background(), c, s, x)
	if e != nil || mutation.ExitCode == 0 {
		t.Fatalf("source-changing check passed %+v %v", mutation, e)
	}
}
func TestCoverage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "coverage.out")
	write(t, p, "mode: set\na.go:1.1,2.1 3 1\na.go:3.1,4.1 1 0\n")
	n, e := CoveragePercent(p)
	if e != nil || n != 75 {
		t.Fatalf("%f %v", n, e)
	}
}

func TestSnapshotIncludesExportIgnoredSource(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, ".gitattributes"), "source.txt export-ignore\n")
	if _, e := git(context.Background(), dir, nil, "add", "."); e != nil {
		t.Fatal(e)
	}
	if _, e := git(context.Background(), dir, nil, "commit", "-qm", "attributes"); e != nil {
		t.Fatal(e)
	}
	s, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(s.Dir, "source.txt")); e != nil || string(b) != "original\n" {
		t.Fatalf("export attributes omitted source: %s %v", b, e)
	}
}
func TestFrozenBaseCannotFollowDirtyWorkspace(t *testing.T) {
	dir := repo(t)
	base, e := ResolveBase(context.Background(), dir, "")
	if e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(dir, "source.txt"), "new candidate\n")
	s, e := FreezeRevision(context.Background(), dir, filepath.Join(t.TempDir(), "base"), base)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifySnapshot(s); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(s.Dir, "source.txt"))
	if e != nil || string(b) != "original\n" {
		t.Fatalf("base included live change: %s %v", b, e)
	}
}
func TestExternalSymlinksRejected(t *testing.T) {
	dir := repo(t)
	if e := os.Symlink("../../outside", filepath.Join(dir, "escape")); e != nil {
		t.Fatal(e)
	}
	if _, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot")); e == nil {
		t.Fatal("accepted external symlink")
	}
}

func TestSnapshotSymlinkChainCannotEscape(t *testing.T) {
	dir := repo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(dir, "sub", "up")); err != nil {
		t.Fatal(err)
	}
	// Lexically sub/up/../outside is internal; after up resolves it escapes.
	if err := os.Symlink("up/../outside", filepath.Join(dir, "sub", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot")); err == nil {
		t.Fatal("accepted escaping symlink chain")
	}
}

func TestNestedManifestChangesSelectEveryTier(t *testing.T) {
	p := Profile{Items: []Check{{ID: "unit", Tier: 1}, {ID: "e2e", Tier: 10}}}
	selected, err := Select(p, Selection{Event: "pr", MaxTier: 1, DiffKnown: true, Paths: []string{"apps/web/package.json"}})
	if err != nil || len(selected) != 2 {
		t.Fatalf("nested manifest omitted checks: %+v %v", selected, err)
	}
}
func TestProfileCannotEscapeThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	p, e := Detect(dir)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(p)
	outside := filepath.Join(t.TempDir(), "checks.json")
	write(t, outside, string(b))
	if e := os.MkdirAll(filepath.Join(dir, ".orquestalite"), 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(dir, DefaultPath)); e != nil {
		t.Fatal(e)
	}
	if _, e = Resolve(dir, ""); e == nil {
		t.Fatal("accepted outside profile")
	}
}
func TestCoverageProducerSelectedAtAnyTier(t *testing.T) {
	p := Profile{Items: []Check{{ID: "coverage", Tier: 10}}, Coverage: Coverage{Threshold: 80, CheckID: "coverage"}}
	selected, e := Select(p, Selection{Event: "pr", MaxTier: 1, DiffKnown: true})
	if e != nil || len(selected) != 1 {
		t.Fatalf("missing coverage producer: %+v %v", selected, e)
	}
}
func TestTimeoutAlwaysLeavesReceipt(t *testing.T) {
	dir := repo(t)
	s, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	x := Execution{Root: t.TempDir(), RunID: "timeout", Scope: "step", Attempt: 1, ProfileHash: Hash([]byte("profile"))}
	ev, e := Execute(context.Background(), Check{ID: "timeout", Argv: []string{"sh", "-c", "sleep 30"}, TimeoutSeconds: 1}, s, x)
	if e != nil {
		t.Fatal(e)
	}
	if !ev.TimedOut || ev.ExitCode == 0 {
		t.Fatalf("timeout marked passed: %+v", ev)
	}
	if e = VerifyReceipt(ev.Receipt, s.TreeHash, x.ProfileHash, false); e != nil {
		t.Fatal(e)
	}
	if e = VerifyEvidence(ev.Receipt, s.TreeHash, x.ProfileHash); e == nil {
		t.Fatal("timeout accepted as passing")
	}
}

func TestApprovedProfileIsSourceButRuntimeIsExcluded(t *testing.T) {
	dir := repo(t)
	write(t, filepath.Join(dir, DefaultPath), `{"profile":"approved"}`)
	write(t, filepath.Join(dir, ".orquestalite", "run.log"), "runtime")
	s, e := Freeze(context.Background(), dir, filepath.Join(t.TempDir(), "snapshot"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.Dir, DefaultPath)); e != nil {
		t.Fatal("approved check source omitted", e)
	}
	if _, e = os.Stat(filepath.Join(s.Dir, ".orquestalite", "run.log")); !os.IsNotExist(e) {
		t.Fatal("runtime state leaked")
	}
	found := false
	for _, p := range s.Paths {
		if p == DefaultPath {
			found = true
		}
	}
	if !found {
		t.Fatal("profile diff invisible to tier selector")
	}
}
