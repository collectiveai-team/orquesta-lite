// Package checks defines the repository's compiled verification contract.
package checks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const DefaultPath = ".orquestalite/checks.json"
const DefaultTimeoutSeconds = 600

type Check struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Surface        string   `json:"surface"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeout_s,omitempty"`
	Required       bool     `json:"required"`
	Tier           int      `json:"tier"`
	Paths          []string `json:"paths,omitempty"`
}
type Coverage struct {
	Threshold float64 `json:"threshold"`
	CheckID   string  `json:"check_id,omitempty"`
	Report    string  `json:"report,omitempty"`
	Format    string  `json:"format,omitempty"`
}
type Profile struct {
	SchemaVersion int      `json:"schema_version"`
	Detected      string   `json:"detected"`
	ManifestsHash string   `json:"manifests_hash"`
	Items         []Check  `json:"items"`
	Coverage      Coverage `json:"coverage"`
}
type Resolved struct {
	Profile      Profile `json:"profile"`
	SourceHash   string  `json:"source_hash"`
	ManifestHash string  `json:"manifest_hash"`
}

func Hash(raw []byte) string { d := sha256.Sum256(raw); return hex.EncodeToString(d[:]) }
func Decode(raw []byte) (*Profile, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("checks: profile exceeds 1 MiB")
	}
	if err := uniqueKeys(raw); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("checks: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("checks: trailing JSON")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	for _, key := range []string{"schema_version", "detected", "manifests_hash", "items", "coverage"} {
		if _, ok := top[key]; !ok {
			return nil, fmt.Errorf("checks: %s is required", key)
		}
	}
	var coverage map[string]json.RawMessage
	if err := json.Unmarshal(top["coverage"], &coverage); err != nil {
		return nil, err
	}
	if _, ok := coverage["threshold"]; !ok {
		return nil, fmt.Errorf("checks: coverage.threshold is required")
	}
	var shape struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	for _, item := range shape.Items {
		for _, field := range []string{"id", "kind", "surface", "argv", "required", "tier"} {
			if _, ok := item[field]; !ok {
				return nil, fmt.Errorf("checks: item field %s is required", field)
			}
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// uniqueKeys rejects the other ambiguous JSON spelling: duplicate object keys.
func uniqueKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var visit func() error
	visit = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if tok == nil {
			return fmt.Errorf("checks: null fields are not allowed")
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				k := key.(string)
				if seen[k] {
					return fmt.Errorf("checks: duplicate field %q", k)
				}
				seen[k] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("checks: unexpected delimiter")
		}
		_, err = d.Token()
		return err
	}
	return visit()
}
func (p Profile) Validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("checks: schema_version must be 1")
	}
	if _, err := time.Parse("2006-01-02", p.Detected); err != nil {
		return fmt.Errorf("checks: detected must be YYYY-MM-DD")
	}
	if len(p.ManifestsHash) != 64 {
		return fmt.Errorf("checks: manifests_hash must be SHA-256")
	}
	if _, err := hex.DecodeString(p.ManifestsHash); err != nil {
		return fmt.Errorf("checks: invalid manifests_hash")
	}
	if p.Items == nil {
		return fmt.Errorf("checks: items must be an explicit array")
	}
	seen := map[string]bool{}
	for _, c := range p.Items {
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]*$`).MatchString(c.ID) || seen[c.ID] {
			return fmt.Errorf("checks: invalid or duplicate id %q", c.ID)
		}
		seen[c.ID] = true
		if !contains([]string{"unit", "integration", "e2e", "lint", "typecheck", "build"}, c.Kind) {
			return fmt.Errorf("checks: %s invalid kind", c.ID)
		}
		if !contains([]string{"back", "front", "shared"}, c.Surface) {
			return fmt.Errorf("checks: %s invalid surface", c.ID)
		}
		if len(c.Argv) == 0 || strings.TrimSpace(c.Argv[0]) == "" {
			return fmt.Errorf("checks: %s argv required", c.ID)
		}
		for _, arg := range c.Argv {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("checks: %s argv contains NUL", c.ID)
			}
		}
		if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 86400 || c.Tier < 1 {
			return fmt.Errorf("checks: %s invalid timeout or tier", c.ID)
		}
		for _, pattern := range c.Paths {
			if err := ValidatePattern(pattern); err != nil {
				return fmt.Errorf("checks: %s: %w", c.ID, err)
			}
		}
	}
	c := p.Coverage
	if c.Threshold < 0 || c.Threshold > 100 {
		return fmt.Errorf("checks: coverage threshold must be 0..100")
	}
	if c.Threshold > 0 && (!seen[c.CheckID] || c.Format != "go-cover" || !safeRelative(c.Report)) {
		return fmt.Errorf("checks: coverage requires check_id, safe report path and format go-cover")
	}
	return nil
}
func Load(path string) (*Profile, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return Decode(b)
}
func Resolve(repo, path string) (*Resolved, error) {
	if path == "" {
		path = DefaultPath
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	realRepo, e := filepath.EvalSymlinks(repo)
	if e != nil {
		return nil, e
	}
	realPath, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, e
	}
	rel, e := filepath.Rel(realRepo, realPath)
	if e != nil || !safeRelative(filepath.ToSlash(rel)) {
		return nil, fmt.Errorf("checks: profile must be inside repository")
	}
	b, e := os.ReadFile(realPath)
	if e != nil {
		return nil, e
	}
	p, e := Decode(b)
	if e != nil {
		return nil, e
	}
	h, e := ManifestHash(repo)
	if e != nil {
		return nil, e
	}
	if h != p.ManifestsHash {
		return nil, fmt.Errorf("checks: manifests changed; run checks detect and confirm the proposed profile")
	}
	for i := range p.Items {
		if p.Items[i].TimeoutSeconds == 0 {
			p.Items[i].TimeoutSeconds = DefaultTimeoutSeconds
		}
	}
	return &Resolved{Profile: *p, SourceHash: Hash(b), ManifestHash: h}, nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func safeRelative(s string) bool {
	return s != "" && !filepath.IsAbs(s) && filepath.ToSlash(filepath.Clean(s)) == s && s != ".." && !strings.HasPrefix(s, "../")
}

var manifestNames = map[string]bool{"package.json": true, "pyproject.toml": true, "Makefile": true, "go.mod": true, "Cargo.toml": true, "pnpm-workspace.yaml": true, "go.sum": true, "uv.lock": true, "pnpm-lock.yaml": true, "package-lock.json": true, "yarn.lock": true, "Cargo.lock": true}

func manifests(repo string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != repo && (strings.HasPrefix(d.Name(), ".") || contains([]string{"node_modules", "vendor", "target", "dist", "build"}, d.Name())) {
			return filepath.SkipDir
		}
		if !d.IsDir() && manifestNames[d.Name()] {
			rel, e := filepath.Rel(repo, path)
			if e != nil {
				return e
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}
func ManifestHash(repo string) (string, error) {
	paths, e := manifests(repo)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	for _, p := range paths {
		full := filepath.Join(repo, p)
		info, e := os.Lstat(full)
		if e != nil {
			return "", e
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("checks: manifest %s must be a regular file", p)
		}
		if info.Size() > 8<<20 {
			return "", fmt.Errorf("checks: manifest exceeds 8 MiB: %s", p)
		}
		b, e := os.ReadFile(full)
		if e != nil {
			return "", e
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Detect only reads manifests. It neither executes scripts nor saves approval.
func Detect(repo string) (*Profile, error) {
	h, e := ManifestHash(repo)
	if e != nil {
		return nil, e
	}
	p := &Profile{SchemaVersion: 1, Detected: time.Now().UTC().Format("2006-01-02"), ManifestsHash: h, Items: []Check{}}
	paths, e := manifests(repo)
	if e != nil {
		return nil, e
	}
	for _, path := range paths {
		dir := filepath.ToSlash(filepath.Dir(path))
		prefix := strings.ReplaceAll(strings.Trim(dir, "."), "/", "-")
		if prefix != "" {
			prefix += "-"
		}
		add := func(id, kind, surface string, argv []string) {
			p.Items = append(p.Items, Check{ID: prefix + id, Kind: kind, Surface: surface, Argv: argv, TimeoutSeconds: 600, Required: true, Tier: 1})
		}
		switch filepath.Base(path) {
		case "go.mod":
			argv := []string{"go", "test", "./..."}
			if dir != "." {
				argv = []string{"go", "-C", dir, "test", "./..."}
			}
			add("unit-go", "unit", "back", argv)
		case "pyproject.toml":
			if dir == "." {
				b, _ := os.ReadFile(filepath.Join(repo, path))
				if bytes.Contains(b, []byte("pytest")) {
					add("unit-python", "unit", "back", []string{"uv", "run", "pytest"})
				}
			}
		case "Cargo.toml":
			argv := []string{"cargo", "test", "--manifest-path", path}
			add("unit-rust", "unit", "back", argv)
			add("build-rust", "build", "back", []string{"cargo", "build", "--manifest-path", path})
		case "Makefile":
			b, err := os.ReadFile(filepath.Join(repo, path))
			if err != nil {
				return nil, err
			}
			for _, target := range []string{"test", "lint", "typecheck", "build", "e2e"} {
				pattern := regexp.MustCompile(`(?m)^` + target + `\s*:`)
				if !pattern.Match(b) {
					continue
				}
				kind := target
				if kind == "test" {
					kind = "unit"
				}
				argv := []string{"make", "-C", dir, target}
				add("make-"+target, kind, "shared", argv)
				if target == "e2e" {
					p.Items[len(p.Items)-1].Tier = 2
				}
			}
		case "package.json":
			b, e := os.ReadFile(filepath.Join(repo, path))
			if e != nil {
				return nil, e
			}
			var pkg struct {
				Scripts        map[string]string `json:"scripts"`
				PackageManager string            `json:"packageManager"`
			}
			if e = json.Unmarshal(b, &pkg); e != nil {
				return nil, fmt.Errorf("checks detect: %s: %w", path, e)
			}
			manager, err := detectPackageManager(repo, pkg.PackageManager)
			if err != nil {
				return nil, fmt.Errorf("checks detect: %s: %w", path, err)
			}
			for _, s := range []string{"test", "lint", "typecheck", "build", "e2e"} {
				if _, ok := pkg.Scripts[s]; !ok {
					continue
				}
				kind := s
				if s == "test" {
					kind = "unit"
				}
				argv := []string{manager, "run", s}
				if dir != "." {
					flag := "--prefix"
					if manager == "pnpm" {
						flag = "--dir"
					} else if manager == "yarn" {
						flag = "--cwd"
					}
					argv = []string{manager, flag, dir, "run", s}
				}
				p.Items = append(p.Items, Check{ID: prefix + s, Kind: kind, Surface: "front", Argv: argv, TimeoutSeconds: 600, Required: true, Tier: 1})
				if s == "e2e" {
					p.Items[len(p.Items)-1].Tier = 2
				}
			}
		}
	}
	return p, p.Validate()
}

func detectPackageManager(repo, declared string) (string, error) {
	if declared == "" {
		var root struct {
			PackageManager string `json:"packageManager"`
		}
		if raw, err := os.ReadFile(filepath.Join(repo, "package.json")); err == nil {
			if err := json.Unmarshal(raw, &root); err != nil {
				return "", err
			}
			declared = root.PackageManager
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if declared != "" {
		manager, _, _ := strings.Cut(declared, "@")
		switch manager {
		case "npm", "pnpm", "yarn":
			return manager, nil
		default:
			return "", fmt.Errorf("unsupported packageManager %q; declare checks manually", declared)
		}
	}
	for _, manager := range []struct{ name, lock string }{{"pnpm", "pnpm-lock.yaml"}, {"yarn", "yarn.lock"}} {
		if _, err := os.Stat(filepath.Join(repo, manager.lock)); err == nil {
			return manager.name, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "npm", nil
}
