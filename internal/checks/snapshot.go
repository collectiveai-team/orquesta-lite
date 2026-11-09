package checks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Snapshot struct {
	Dir          string   `json:"dir"`
	TreeHash     string   `json:"tree_hash"`
	Commit       string   `json:"commit"`
	BaseRevision string   `json:"base_revision"`
	Paths        []string `json:"paths"`
	ChangedLines int      `json:"changed_lines"`
	DiffKnown    bool     `json:"diff_known"`
	FilesHash    string   `json:"files_hash"`
}

func git(ctx context.Context, repo string, env []string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = repo
	c.Env = append(os.Environ(), env...)
	b, e := c.CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("checks git %s: %w: %s", args[0], e, b)
	}
	return b, nil
}
func Freeze(ctx context.Context, repo, destination string) (Snapshot, error) {
	return FreezeBase(ctx, repo, destination, "")
}
func FreezeBase(ctx context.Context, repo, destination, base string) (Snapshot, error) {
	var s Snapshot
	root, e := git(ctx, repo, nil, "rev-parse", "--show-toplevel")
	if e != nil {
		return s, e
	}
	repo = strings.TrimSpace(string(root))
	commit, e := git(ctx, repo, nil, "rev-parse", "HEAD")
	if e != nil {
		return s, e
	}
	s.Commit = strings.TrimSpace(string(commit))
	if base == "" {
		base = s.Commit
	}
	if strings.HasPrefix(base, "-") {
		return s, fmt.Errorf("checks: invalid base revision")
	}
	resolved, e := git(ctx, repo, nil, "rev-parse", "--verify", base+"^{commit}")
	if e != nil {
		return s, e
	}
	s.BaseRevision = strings.TrimSpace(string(resolved))
	s.TreeHash, e = sourceTree(ctx, repo)
	if e != nil {
		return s, e
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0755); e != nil {
		return s, e
	}
	if e = os.Mkdir(destination, 0755); e != nil {
		return s, e
	}
	s.Dir, e = filepath.Abs(destination)
	if e != nil {
		return s, e
	}
	if e = exportTree(ctx, repo, s.TreeHash, s.Dir); e != nil {
		return s, e
	}
	diff, e := git(ctx, repo, nil, "diff", "--name-only", "--no-renames", "-z", s.BaseRevision, s.TreeHash, "--")
	if e != nil {
		return s, e
	}
	s.Paths = []string{}
	for _, p := range strings.Split(string(diff), "\x00") {
		if p != "" {
			s.Paths = append(s.Paths, p)
		}
	}
	stats, e := git(ctx, repo, nil, "diff", "--numstat", "--no-renames", s.BaseRevision, s.TreeHash, "--")
	if e != nil {
		return s, e
	}
	for _, line := range strings.Split(string(stats), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		for _, n := range fields[:2] {
			v, err := strconv.Atoi(n)
			if err != nil {
				s.ChangedLines += 1000
			} else {
				s.ChangedLines += v
			}
		}
	}
	s.DiffKnown = true
	files, e := sourceFiles(s.Dir)
	if e != nil {
		return s, e
	}
	raw, e := json.Marshal(files)
	if e != nil {
		return s, e
	}
	s.FilesHash = Hash(raw)
	raw, e = json.MarshalIndent(s, "", "  ")
	if e != nil {
		return s, e
	}
	if e = os.WriteFile(s.Dir+".json", raw, 0600); e != nil {
		return s, e
	}
	return s, nil
}

// CurrentTree measures the live candidate using exactly the source inclusion
// rules used by FreezeBase, without exporting another copy or changing the
// user's real index. Completion compares this hash with its approved snapshot.
func CurrentTree(ctx context.Context, repo string) (string, error) {
	root, err := git(ctx, repo, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return sourceTree(ctx, strings.TrimSpace(string(root)))
}

func sourceTree(ctx context.Context, repo string) (string, error) {
	temp, e := os.MkdirTemp("", "orq-check-index-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(temp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(temp, "index")}
	if _, e = git(ctx, repo, env, "read-tree", "HEAD"); e != nil {
		return "", e
	}
	args := []string{"add", "-A", "--", "."}
	for _, exclude := range []string{".orquestalite", ".tmp", ".scratch", ".worktrees", ".journals"} {
		args = append(args, ":(exclude)"+exclude+"/**", ":(exclude)"+exclude)
	}
	if _, e = git(ctx, repo, env, args...); e != nil {
		return "", e
	}
	if _, e = git(ctx, repo, env, "rm", "-r", "--cached", "--ignore-unmatch", "--", ".orquestalite", ".tmp", ".scratch", ".worktrees", ".journals"); e != nil {
		return "", e
	}
	// The approved checks contract is source; all other runtime state stays out.
	if info, err := os.Lstat(filepath.Join(repo, DefaultPath)); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("checks: profile must be a regular file")
		}
		if _, e = git(ctx, repo, env, "add", "-f", "--", DefaultPath); e != nil {
			return "", e
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tree, e := git(ctx, repo, env, "write-tree")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(tree)), nil
}

func VerifySnapshot(s Snapshot) error {
	files, e := sourceFiles(s.Dir)
	if e != nil {
		return e
	}
	b, e := json.Marshal(files)
	if e != nil {
		return e
	}
	if s.FilesHash == "" || Hash(b) != s.FilesHash {
		return fmt.Errorf("checks: snapshot content changed")
	}
	raw, e := os.ReadFile(s.Dir + ".json")
	if e != nil {
		return e
	}
	var saved Snapshot
	if e = json.Unmarshal(raw, &saved); e != nil {
		return e
	}
	a, _ := json.Marshal(s)
	b, _ = json.Marshal(saved)
	if string(a) != string(b) {
		return fmt.Errorf("checks: snapshot metadata changed")
	}
	return nil
}

// ResolveBase pins the base before an implementation agent can create commits.
func ResolveBase(ctx context.Context, repo, revision string) (string, error) {
	if revision == "" {
		revision = "HEAD"
	}
	if strings.HasPrefix(revision, "-") {
		return "", fmt.Errorf("checks: invalid base revision")
	}
	b, e := git(ctx, repo, nil, "rev-parse", "--verify", revision+"^{commit}")
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(string(b)), nil
}

// FreezeRevision exports a committed base independently of the live index/tree.
func FreezeRevision(ctx context.Context, repo, destination, revision string) (Snapshot, error) {
	var s Snapshot
	commit, e := ResolveBase(ctx, repo, revision)
	if e != nil {
		return s, e
	}
	b, e := git(ctx, repo, nil, "rev-parse", "--verify", commit+"^{tree}")
	if e != nil {
		return s, e
	}
	s = Snapshot{Commit: commit, BaseRevision: commit, TreeHash: strings.TrimSpace(string(b)), Paths: []string{}, DiffKnown: true}
	s.Dir, e = filepath.Abs(destination)
	if e != nil {
		return s, e
	}
	if e = os.MkdirAll(filepath.Dir(s.Dir), 0755); e != nil {
		return s, e
	}
	if e = os.Mkdir(s.Dir, 0755); e != nil {
		return s, e
	}
	if e = exportTree(ctx, repo, s.TreeHash, s.Dir); e != nil {
		return s, e
	}
	files, e := sourceFiles(s.Dir)
	if e != nil {
		return s, e
	}
	b, e = json.Marshal(files)
	if e != nil {
		return s, e
	}
	s.FilesHash = Hash(b)
	b, e = json.MarshalIndent(s, "", "  ")
	if e != nil {
		return s, e
	}
	if e = os.WriteFile(s.Dir+".json", b, 0600); e != nil {
		return s, e
	}
	return s, nil
}

// exportTree ignores export-ignore/export-subst attributes: every Git blob is
// copied verbatim, so the source really corresponds to the pinned tree.
func exportTree(ctx context.Context, repo, tree, destination string) error {
	listing, err := git(ctx, repo, nil, "ls-tree", "-rz", "--full-tree", tree)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	command.Dir = repo
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var errors bytes.Buffer
	command.Stderr = &errors
	if err = command.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(output)
	export := func() error {
		for _, entry := range bytes.Split(listing, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			parts := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(parts) != 2 {
				return fmt.Errorf("checks: malformed tree entry")
			}
			fields := strings.Fields(string(parts[0]))
			if len(fields) != 3 || fields[1] != "blob" {
				return fmt.Errorf("checks: submodules and nonblob entries are unsupported")
			}
			name := string(parts[1])
			if !safeRelative(name) {
				return fmt.Errorf("checks: unsafe tree entry")
			}
			for _, segment := range strings.Split(name, "/") {
				if strings.EqualFold(segment, ".git") {
					return fmt.Errorf("checks: git metadata in source tree")
				}
			}
			target := filepath.Join(destination, name)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(input, fields[2]); err != nil {
				return err
			}
			header, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			info := strings.Fields(header)
			if len(info) != 3 || info[0] != fields[2] || info[1] != "blob" {
				return fmt.Errorf("checks: invalid blob response")
			}
			size, err := strconv.ParseInt(info[2], 10, 64)
			if err != nil || size < 0 {
				return fmt.Errorf("checks: invalid blob size")
			}
			if fields[0] == "120000" {
				if size > 4096 {
					return fmt.Errorf("checks: symlink target exceeds limit")
				}
				b := make([]byte, size)
				if _, err = io.ReadFull(reader, b); err != nil {
					return err
				}
				link := string(b)
				resolved := filepath.Clean(filepath.Join(filepath.Dir(name), link))
				if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, "../") {
					return fmt.Errorf("checks: external symlink %s", name)
				}
				if err = os.Symlink(link, target); err != nil {
					return err
				}
			} else {
				mode := os.FileMode(0644)
				if fields[0] == "100755" {
					mode = 0755
				} else if fields[0] != "100644" {
					return fmt.Errorf("checks: unsupported blob mode")
				}
				f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
				if err != nil {
					return err
				}
				_, copyErr := io.CopyN(f, reader, size)
				closeErr := f.Close()
				if copyErr != nil {
					return copyErr
				}
				if closeErr != nil {
					return closeErr
				}
			}
			newline, err := reader.ReadByte()
			if err != nil || newline != '\n' {
				return fmt.Errorf("checks: malformed blob terminator")
			}
		}
		return nil
	}
	err = export()
	_ = input.Close()
	if err != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if err != nil {
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("checks: export failed: %w: %s", waitErr, errors.String())
	}
	return nil
}
