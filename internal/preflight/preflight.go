package preflight

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lionelchamorro/orquestalite/internal/tasks"
)

// Verdict is the result of a pre-flight check.
type Verdict struct {
	OK     bool
	Reason string // human-readable, e.g. "description too short", "references missing files: [..]"
}

// urlRe matches URL prefixes so they can be stripped before path extraction.
var urlRe = regexp.MustCompile(`https?://\S+`)

// pathRe matches file-like tokens: alphanumeric/underscore/dot/hyphen sequences that
// contain at least one dot (extension) and may contain slashes (path separators).
var pathRe = regexp.MustCompile(`[a-zA-Z0-9_./-]+\.[a-zA-Z0-9]+`)

// Check runs lightweight, conservative validation on a task.
//
// It only flags signals that are clearly problematic:
//   - description shorter than 50 characters (after whitespace trim)
//   - file paths mentioned in description that look real (contain "/") but
//     do not exist on disk under workdir
//
// Returns Verdict{OK: true} when nothing is obviously wrong.
func Check(workdir string, t *tasks.Task) Verdict {
	desc := strings.TrimSpace(t.Description)

	// 3a — description length check
	if utf8.RuneCountInString(desc) < 50 {
		return Verdict{OK: false, Reason: "description too short (<50 chars after trim)"}
	}

	// 3b — file reference check
	// Pre-scrub URLs so example.com/foo.html does not trigger a false positive.
	scrubbed := urlRe.ReplaceAllString(desc, "")

	matches := pathRe.FindAllString(scrubbed, -1)
	var missing []string
	seen := map[string]bool{}
	for _, m := range matches {
		// Only consider path-shaped tokens (must contain "/").
		if !strings.Contains(m, "/") {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		_, err := os.Stat(fmt.Sprintf("%s/%s", workdir, m))
		if os.IsNotExist(err) {
			missing = append(missing, m)
		}
	}

	if len(missing) > 0 {
		return Verdict{
			OK:     false,
			Reason: fmt.Sprintf("references missing files: [%s]", strings.Join(missing, ", ")),
		}
	}

	return Verdict{OK: true}
}
