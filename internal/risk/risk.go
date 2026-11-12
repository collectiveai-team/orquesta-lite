// Package risk pins deterministic review scope to the first candidate of a unit.
package risk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
)

type Policy struct {
	Version        string   `json:"version"`
	SensitivePaths []string `json:"sensitive_paths"`
	StandardLines  int      `json:"standard_lines"`
	HighLines      int      `json:"high_lines"`
}

func DefaultPolicy() Policy {
	return Policy{Version: "1", SensitivePaths: []string{"**/auth/**", "**/payments/**", "**/migrations/**", "**/contracts/**", "**/concurrency/**"}, StandardLines: 50, HighLines: 400}
}

func (p Policy) Validate() error {
	if p.Version == "" || p.StandardLines < 1 || p.HighLines < p.StandardLines {
		return fmt.Errorf("risk policy requires version and 0 < standard_lines <= high_lines")
	}
	for _, pattern := range p.SensitivePaths {
		if _, err := match(pattern, "probe"); err != nil || pattern == "" {
			return fmt.Errorf("invalid sensitive path pattern %q", pattern)
		}
	}
	return nil
}

type Candidate struct {
	TreeHash       string   `json:"tree_hash"`
	BaseRevision   string   `json:"base_revision"`
	Paths          []string `json:"paths"`
	ChangedLines   int      `json:"changed_lines"`
	CoverageKnown  bool     `json:"coverage_known"`
	UncoveredPaths []string `json:"uncovered_paths"`
}

type Decision struct {
	Version        int       `json:"version"`
	PolicyDigest   string    `json:"policy_digest"`
	Candidate      Candidate `json:"candidate"`
	Level          string    `json:"level"`
	Structural     bool      `json:"structural"`
	Lenses         []string  `json:"lenses"`
	Reasons        []string  `json:"reasons"`
	SensitivePaths []string  `json:"sensitive_paths"`
}

func Classify(c Candidate, p Policy) (Decision, error) {
	if err := p.Validate(); err != nil {
		return Decision{}, err
	}
	if c.TreeHash == "" || c.BaseRevision == "" || c.ChangedLines < 0 {
		return Decision{}, fmt.Errorf("risk requires frozen tree, base revision and nonnegative changed lines")
	}
	for _, name := range append(slices.Clone(c.Paths), c.UncoveredPaths...) {
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return Decision{}, fmt.Errorf("invalid candidate path %q", name)
		}
	}
	c.Paths = unique(c.Paths)
	c.UncoveredPaths = unique(c.UncoveredPaths)
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	d := Decision{Version: 1, PolicyDigest: hex.EncodeToString(sum[:]), Candidate: c, Level: "low", Structural: true, Lenses: []string{}, Reasons: []string{}, SensitivePaths: []string{}}
	for _, name := range c.Paths {
		for _, pattern := range p.SensitivePaths {
			if ok, _ := match(pattern, name); ok {
				d.SensitivePaths = append(d.SensitivePaths, name)
				break
			}
		}
	}
	if len(d.SensitivePaths) > 0 {
		d.Level = "high"
		d.Reasons = append(d.Reasons, "sensitive paths changed")
	}
	if c.ChangedLines >= p.HighLines {
		d.Level = "high"
		d.Reasons = append(d.Reasons, "diff exceeds high-risk line threshold")
	} else if c.ChangedLines >= p.StandardLines {
		if d.Level == "low" {
			d.Level = "standard"
		}
		d.Reasons = append(d.Reasons, "diff exceeds standard line threshold")
	}
	if !c.CoverageKnown || len(c.UncoveredPaths) > 0 {
		if d.Level == "low" {
			d.Level = "standard"
		}
		if !c.CoverageKnown {
			d.Reasons = append(d.Reasons, "test coverage mapping is unknown")
		} else {
			d.Reasons = append(d.Reasons, "changed area has no mapped test")
		}
	}
	if d.Level == "standard" {
		d.Lenses = []string{"reliability"}
	} else if d.Level == "high" {
		d.Lenses = []string{"risk", "resilience", "readability", "reliability"}
	}
	if len(d.Reasons) == 0 {
		d.Reasons = append(d.Reasons, "small change with mapped tests outside sensitive paths")
	}
	return d, nil
}

// CheckScope never reclassifies the unit after repair. A new sensitive path is
// a scope expansion requiring a person, even when the original risk was high.
func CheckScope(original Decision, next Candidate, p Policy) error {
	d, err := Classify(next, p)
	if err != nil {
		return err
	}
	if d.PolicyDigest != original.PolicyDigest {
		return fmt.Errorf("needs_decision: risk policy changed during unit")
	}
	if next.BaseRevision != original.Candidate.BaseRevision {
		return fmt.Errorf("needs_decision: unit base revision changed")
	}
	for _, name := range d.SensitivePaths {
		if !slices.Contains(original.SensitivePaths, name) {
			return fmt.Errorf("needs_decision: correction added sensitive path %s", name)
		}
	}
	return nil
}

func unique(values []string) []string {
	out := slices.Clone(values)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// match implements slash-separated globstar; ** may consume zero components.
func match(pattern, name string) (bool, error) {
	parts := strings.Split(pattern, "/")
	names := strings.Split(name, "/")
	for _, part := range parts {
		if part != "**" {
			if _, err := path.Match(part, ""); err != nil {
				return false, err
			}
		}
	}
	var walk func(int, int) bool
	walk = func(i, j int) bool {
		if i == len(parts) {
			return j == len(names)
		}
		if parts[i] == "**" {
			return walk(i+1, j) || (j < len(names) && walk(i, j+1))
		}
		if j == len(names) {
			return false
		}
		ok, _ := path.Match(parts[i], names[j])
		return ok && walk(i+1, j+1)
	}
	return walk(0, 0), nil
}
