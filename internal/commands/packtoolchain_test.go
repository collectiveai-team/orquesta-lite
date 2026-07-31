package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hardcodedToolchainPattern is the parity rule TestGovernedPackHasNoHardcodedToolchain
// applies to every file in the pack. It lives here, as one shared helper, so it
// can be tested for teeth instead of being re-inlined at each call site.
//
// It has to be token-aware. The rule started life as the substring `uv ` — `uv`
// followed by a space — which matches the prose form that appears in READMEs
// ("uv run ruff check .") and misses the executable form that appears in flows:
//
//	"argv": ["uv", "run", "pytest", "-q"]
//
// encodes as `"uv",` and never produces `uv ` at all. Running the old rule over
// the pre-v4 subflows/integrated-review@1.json returns zero hits — the gate
// passed cleanly on the exact file it existed to reject, so the next person to
// paste a Python gate back into a shared subflow would have shipped it green.
//
// The two alternations below are the two encodings a toolchain invocation can
// take in this pack: a JSON string element (`"uv"`), and a standalone shell
// word in prose or a command string (`uv run ...`).
var hardcodedToolchainPattern = regexp.MustCompile(`"(uv|pytest|ruff|npm|npx|poetry|pipenv)"|(^|["'` + "`" + `\s;&|(])(uv|pytest|ruff|npm|npx|poetry|pipenv)(\s|$)`)

// packHardcodesToolchain reports the first hardcoded toolchain token in text,
// or "" when there is none.
func packHardcodesToolchain(text string) string {
	return hardcodedToolchainPattern.FindString(text)
}

// TestPackToolchainGateDetectsTheArgvForm is the audit of the parity gate the
// design asked for ("dos greps que tienen que dar cero"): the rule must flag
// both encodings of a gate the v4 change removed, and must stay quiet on the
// shipped pack.
func TestPackToolchainGateDetectsTheArgvForm(t *testing.T) {
	legacy := []string{
		`{"id":"tests","uses":"activity:gate.run@1","with":{"argv":["uv","run","pytest","-q"]}}`,
		`  "uv",`,
		`  "ruff",`,
		"run the suite with uv run pytest -q before committing",
		`{"full_test_command":"npm test --silent"}`,
	}
	for _, text := range legacy {
		if packHardcodesToolchain(text) == "" {
			t.Errorf("the parity gate does not flag a hardcoded toolchain: %s", text)
		}
	}
	// Words that merely contain a tool name are not invocations of it.
	for _, benign := range []string{
		`{"note":"the universe is large"}`,
		"see docs/ruffle.md",
		`{"argv":{"$ref":"config.test_argv"}}`,
		`"lint_argv"`,
	} {
		if hit := packHardcodesToolchain(benign); hit != "" {
			t.Errorf("%s: false positive on %q", benign, hit)
		}
	}
	// The shipped pack must stay clean under the rule that now has teeth.
	assertPackHasNoHardcodedToolchain(t, governedPackRoot(t))
}

// assertPackHasNoHardcodedToolchain walks a pack and fails on any file that
// bakes in a project's toolchain. Shared by both parity tests: uninstalling a
// pack cannot remove a gate baked into a subflow, which is why this covers the
// whole pack and not just its flows.
func assertPackHasNoHardcodedToolchain(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if hit := packHardcodesToolchain(line); hit != "" {
				relative, _ := filepath.Rel(root, path)
				t.Errorf("%s hardcodes %q; gates come from team.json now (config.lint_argv / config.test_argv)", relative, strings.TrimSpace(hit))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
