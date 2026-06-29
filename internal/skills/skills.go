// Package skills discovers and parses project-defined skills: versioned
// markdown files in a `skills/` directory, each holding a name, a description,
// and a procedure. The orchestrator injects a task's requested skills into the
// coder/critic prompts as {{SKILLS}}, so a plan can name the working style an
// agent must follow (e.g. "tdd") without baking it into every prompt.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one project-defined procedure: a name (the lookup key), a short
// description, and the procedure body the agent follows.
type Skill struct {
	Name        string
	Description string
	Procedure   string
}

// Registry is the set of skills discovered in a skills/ directory, keyed by
// skill name.
type Registry struct {
	skills map[string]Skill
	dir    string
}

// DefaultDir is the project-relative directory scanned for skill files.
const DefaultDir = "skills"

// Load discovers and parses every skill file in dir (a skills/ directory).
// Returns an empty registry when the directory is missing — requesting a skill
// then surfaces a clear "not found" error rather than a directory error. Files
// without a parsable name are skipped with a collected error so a malformed
// skill file is visible but does not abort the whole registry.
func Load(dir string) (*Registry, error) {
	r := &Registry{skills: map[string]Skill{}, dir: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}
	var skips []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := parseFile(path)
		if err != nil {
			skips = append(skips, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		r.skills[s.Name] = s
	}
	if len(skips) > 0 {
		return r, fmt.Errorf("skipped unparseable skill files: %s", strings.Join(skips, "; "))
	}
	return r, nil
}

// parseFile reads one skill markdown file and extracts its name, description,
// and procedure. The file format is a YAML-like front matter delimited by `---`
// lines, followed by the procedure body:
//
//	---
//	name: tdd
//	description: Test-driven development loop.
//	---
//	<procedure body>
//
// When `name:` is absent the file stem (without .md) is used, so a bare
// procedure file still registers under its filename.
func parseFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("read %s: %w", path, err)
	}
	name, description, procedure := splitFrontMatter(string(raw))
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return Skill{
		Name:        name,
		Description: description,
		Procedure:   strings.TrimSpace(procedure),
	}, nil
}

// splitFrontMatter parses the `---`-delimited header and returns the name,
// description, and the remaining procedure body. When no front matter is
// present the whole file is the procedure and name/description are empty.
func splitFrontMatter(raw string) (name, description, procedure string) {
	body := raw
	if strings.HasPrefix(raw, "---\n") || strings.HasPrefix(raw, "---\r\n") {
		rest := strings.TrimPrefix(raw, "---\n")
		rest = strings.TrimPrefix(rest, "---\r\n")
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			header := rest[:idx]
			procedure = strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
			for _, line := range strings.Split(header, "\n") {
				if v, ok := kv(line, "name"); ok {
					name = v
				} else if v, ok := kv(line, "description"); ok {
					description = v
				}
			}
			return name, description, procedure
		}
		body = rest
	}
	return "", "", body
}

// kv extracts the trimmed value of a "key: value" line, returning false when
// the line is not that key.
func kv(line, key string) (string, bool) {
	prefix := key + ":"
	l := strings.TrimSpace(line)
	if !strings.HasPrefix(l, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(l, prefix)), true
}

// Names returns the sorted names of all discovered skills.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.skills))
	for n := range r.skills {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Get returns the skill named name, or false when it is not in the registry.
func (r *Registry) Get(name string) (Skill, bool) {
	s, ok := r.skills[name]
	return s, ok
}

// Render returns the {{SKILLS}} text for a task's requested skills: each
// requested skill's name, description, and procedure, separated by headers. A
// missing skill is an immediate, clear error so a typo in a plan cannot silently
// drop the working style. An empty list yields a placeholder telling the agent
// no skills were requested.
func (r *Registry) Render(names []string) (string, error) {
	if len(names) == 0 {
		return "(no skills requested for this task)", nil
	}
	// De-duplicate while preserving the first-seen order so the prompt is stable.
	seen := make(map[string]bool, len(names))
	ordered := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		ordered = append(ordered, n)
	}
	var b strings.Builder
	for i, n := range ordered {
		s, ok := r.skills[n]
		if !ok {
			avail := strings.Join(r.Names(), ", ")
			if avail == "" {
				avail = "(no skills defined in " + r.dir + ")"
			}
			return "", fmt.Errorf("skill %q not found; available: %s", n, avail)
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "### Skill: %s\n%s\n\n%s", s.Name, s.Description, s.Procedure)
	}
	return b.String(), nil
}
