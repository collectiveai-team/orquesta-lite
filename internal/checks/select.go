package checks

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

type Selection struct {
	Event     string   `json:"event"`
	MaxTier   int      `json:"max_tier"`
	Paths     []string `json:"paths"`
	DiffKnown bool     `json:"diff_known"`
}

func ValidatePattern(pattern string) error {
	if !safeRelative(pattern) || strings.Contains(pattern, "\\") {
		return fmt.Errorf("invalid relative glob %q", pattern)
	}
	_, err := glob(pattern)
	return err
}
func glob(pattern string) (*regexp.Regexp, error) {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		case '[':
			return nil, fmt.Errorf("checks: character classes are unsupported in path globs")
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}
func Match(pattern, file string) bool {
	r, e := glob(pattern)
	return e == nil && r.MatchString(path.Clean(file))
}
func Select(p Profile, s Selection) ([]Check, error) {
	if s.Event != "pr" && s.Event != "merge" && s.Event != "release" {
		return nil, fmt.Errorf("checks: event must be pr, merge or release")
	}
	if s.MaxTier < 0 {
		return nil, fmt.Errorf("checks: negative max_tier")
	}
	all := s.Event != "pr" || !s.DiffKnown
	for _, f := range s.Paths {
		if f == DefaultPath || manifestNames[path.Base(f)] || contains([]string{"go.sum", "pnpm-lock.yaml", "package-lock.json", "uv.lock", "Cargo.lock"}, path.Base(f)) || strings.HasPrefix(f, ".github/") {
			all = true
		}
	}
	out := []Check{}
	for _, c := range p.Items {
		selected := all || c.Tier <= s.MaxTier
		for _, pattern := range c.Paths {
			for _, file := range s.Paths {
				selected = selected || Match(pattern, file)
			}
		}
		if c.ID == p.Coverage.CheckID && p.Coverage.Threshold > 0 {
			selected = true
		}
		if selected {
			out = append(out, c)
		}
	}
	return out, nil
}
