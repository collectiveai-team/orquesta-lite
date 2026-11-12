package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/processtree"
)

const MaxLogBytes int64 = 8 << 20
const ExcerptBytes = 4096

type Execution struct {
	Root        string
	RunID       string
	Scope       string
	Attempt     int
	ProfileHash string
}
type Evidence struct {
	CheckID         string   `json:"check_id"`
	Argv            []string `json:"argv"`
	ExitCode        int      `json:"exit_code"`
	DurationSeconds float64  `json:"duration_s"`
	TreeHash        string   `json:"tree_hash"`
	Commit          string   `json:"commit"`
	ProfileHash     string   `json:"profile_hash"`
	RunID           string   `json:"run_id"`
	Scope           string   `json:"scope"`
	Attempt         int      `json:"attempt"`
	Stdout          string   `json:"stdout_excerpt"`
	Stderr          string   `json:"stderr_excerpt"`
	StdoutLog       string   `json:"stdout_log"`
	StderrLog       string   `json:"stderr_log"`
	StdoutHash      string   `json:"stdout_hash"`
	StderrHash      string   `json:"stderr_hash"`
	Error           string   `json:"error,omitempty"`
	TimedOut        bool     `json:"timed_out"`
	Receipt         string   `json:"receipt"`
	ReceiptHash     string   `json:"-"`
}
type logWriter struct {
	file     *os.File
	written  int64
	excerpt  bytes.Buffer
	exceeded bool
}

func (w *logWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.written+int64(len(p)) > MaxLogBytes {
		w.exceeded = true
		remaining := MaxLogBytes - w.written
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	if w.excerpt.Len() < ExcerptBytes {
		n := ExcerptBytes - w.excerpt.Len()
		if n > len(p) {
			n = len(p)
		}
		w.excerpt.Write(p[:n])
	}
	n, e := w.file.Write(p)
	w.written += int64(n)
	if e != nil {
		return n, e
	}
	if w.exceeded {
		return original, fmt.Errorf("check output exceeds %d bytes", MaxLogBytes)
	}
	return original, nil
}
func Execute(ctx context.Context, c Check, s Snapshot, x Execution) (Evidence, error) {
	e := Evidence{CheckID: c.ID, Argv: c.Argv, ExitCode: -1, TreeHash: s.TreeHash, Commit: s.Commit, ProfileHash: x.ProfileHash, RunID: x.RunID, Scope: x.Scope, Attempt: x.Attempt}
	if len(c.Argv) == 0 {
		return e, fmt.Errorf("checks: empty argv")
	}
	if x.Attempt < 1 || x.ProfileHash == "" || s.TreeHash == "" {
		return e, fmt.Errorf("checks: execution identity incomplete")
	}
	if err := VerifySnapshot(s); err != nil {
		return e, err
	}
	identity := Hash([]byte(x.RunID + "\x00" + x.Scope + "\x00" + c.ID))
	dir := filepath.Join(x.Root, "checks", identity, fmt.Sprintf("attempt-%d", x.Attempt))
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return e, err
	}
	if err := os.Mkdir(dir, 0755); err != nil {
		return e, fmt.Errorf("checks: evidence attempt already exists or cannot be created: %w", err)
	}
	work := filepath.Join(dir, "tree")
	if err := copyTree(s.Dir, work); err != nil {
		return e, err
	}
	before, err := sourceFiles(s.Dir)
	if err != nil {
		return e, err
	}
	e.StdoutLog = filepath.Join(dir, "stdout.log")
	e.StderrLog = filepath.Join(dir, "stderr.log")
	out, err := os.OpenFile(e.StdoutLog, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return e, err
	}
	defer out.Close()
	stderr, err := os.OpenFile(e.StderrLog, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return e, err
	}
	defer stderr.Close()
	ow, ew := &logWriter{file: out}, &logWriter{file: stderr}
	timeout := c.TimeoutSeconds
	if timeout <= 0 {
		timeout = DefaultTimeoutSeconds
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(callCtx, c.Argv[0], c.Argv[1:]...)
	processtree.Configure(cmd)
	cmd.Dir = work
	cmd.Stdout = ow
	cmd.Stderr = ew
	started := time.Now()
	runErr := cmd.Run()
	e.DurationSeconds = time.Since(started).Seconds()
	if cmd.ProcessState != nil {
		e.ExitCode = cmd.ProcessState.ExitCode()
	}
	e.TimedOut = callCtx.Err() == context.DeadlineExceeded
	if runErr != nil {
		e.Error = runErr.Error()
	}
	if ow.exceeded || ew.exceeded {
		e.Error = "check output limit exceeded"
		e.ExitCode = -1
	}
	after, readErr := sourceFilesSubset(work, before)
	if readErr != nil || !sameFiles(before, after) {
		e.Error = "check modified or removed source files"
		e.ExitCode = -1
	}
	e.Stdout = ow.excerpt.String()
	e.Stderr = ew.excerpt.String()
	if err := out.Sync(); err != nil {
		return e, err
	}
	if err := stderr.Sync(); err != nil {
		return e, err
	}
	ob, err := os.ReadFile(e.StdoutLog)
	if err != nil {
		return e, err
	}
	eb, err := os.ReadFile(e.StderrLog)
	if err != nil {
		return e, err
	}
	e.StdoutHash = Hash(ob)
	e.StderrHash = Hash(eb)
	e.Receipt = filepath.Join(dir, "evidence.json")
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return e, err
	}
	f, err := os.OpenFile(e.Receipt, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return e, err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return e, err
	}
	if closeErr != nil {
		return e, closeErr
	}
	e.ReceiptHash = Hash(raw)
	return e, nil
}
func sourceFiles(root string) (map[string]string, error) {
	files := map[string]string{}
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if err := validateSymlink(root, p); err != nil {
				return err
			}
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		h, e := fileHash(p)
		if e != nil {
			return e
		}
		files[rel] = h
		return nil
	})
	return files, e
}

// Resolve existing symlink components rather than just cleaning the spelling:
// an apparently internal a/../../target can escape after a resolves elsewhere.
// Dangling internal links are permitted because build outputs may not exist yet.
func validateSymlink(root, name string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return err
	}
	components := strings.Split(filepath.ToSlash(rel), "/")
	current := root
	links := 0
	for len(components) > 0 {
		component := components[0]
		components = components[1:]
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			if current == root {
				return fmt.Errorf("checks: symlink escapes snapshot: %s", rel)
			}
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, component)
		info, statErr := os.Lstat(candidate)
		if os.IsNotExist(statErr) {
			current = candidate
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = candidate
			continue
		}
		links++
		if links > 40 {
			return fmt.Errorf("checks: symlink cycle in snapshot: %s", rel)
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			return fmt.Errorf("checks: absolute symlink in snapshot: %s", rel)
		}
		components = append(strings.Split(filepath.ToSlash(target), "/"), components...)
	}
	return nil
}
func fileHash(p string) (string, error) {
	info, e := os.Lstat(p)
	if e != nil {
		return "", e
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, e := os.Readlink(p)
		return "link:" + target, e
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("checks: nonregular file %s", p)
	}
	file, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer file.Close()
	h := sha256.New()
	if _, e = io.Copy(h, file); e != nil {
		return "", e
	}
	return strconv.FormatUint(uint64(info.Mode().Perm()), 8) + ":" + hex.EncodeToString(h.Sum(nil)), nil
}
func sourceFilesSubset(root string, files map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for p := range files {
		h, e := fileHash(filepath.Join(root, p))
		if e != nil {
			return out, e
		}
		out[p] = h
	}
	return out, nil
}
func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for p, h := range a {
		if b[p] != h {
			return false
		}
	}
	return true
}
func copyTree(from, to string) error {
	if e := os.Mkdir(to, 0755); e != nil {
		return e
	}
	return filepath.WalkDir(from, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(from, p)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.Mkdir(dest, 0755)
		}
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil {
				return e
			}
			return os.Symlink(target, dest)
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		ce := out.Close()
		if e != nil {
			return e
		}
		return ce
	})
}
func VerifyEvidence(path, treeHash, profileHash string) error {
	return VerifyReceipt(path, treeHash, profileHash, true)
}
func VerifyReceipt(path, treeHash, profileHash string, requirePass bool) error {
	raw, e := os.ReadFile(path)
	if e != nil {
		return fmt.Errorf("checks: missing evidence: %w", e)
	}
	var receipt Evidence
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&receipt); e != nil {
		return e
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("checks: trailing evidence JSON")
	}
	if receipt.Receipt != path || receipt.TreeHash != treeHash || receipt.ProfileHash != profileHash {
		return fmt.Errorf("checks: evidence identity mismatch")
	}
	if requirePass && (receipt.ExitCode != 0 || receipt.Error != "" || receipt.TimedOut) {
		return fmt.Errorf("checks: evidence does not prove a passing check")
	}
	for _, log := range []struct{ path, hash string }{{receipt.StdoutLog, receipt.StdoutHash}, {receipt.StderrLog, receipt.StderrHash}} {
		b, e := os.ReadFile(log.path)
		if e != nil || Hash(b) != log.hash {
			return fmt.Errorf("checks: missing or corrupt log %s", log.path)
		}
	}
	return nil
}

// CoveragePercent reads Go's native coverprofile format, counting statements.
func CoveragePercent(path string) (float64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxLogBytes {
		return 0, fmt.Errorf("checks: coverage report must be regular and at most %d bytes", MaxLogBytes)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return 0, e
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "mode: ") {
		return 0, fmt.Errorf("checks: invalid or empty Go coverage report")
	}
	total, covered := 0, 0
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return 0, fmt.Errorf("checks: invalid coverage row")
		}
		n, e := strconv.Atoi(fields[1])
		if e != nil || n < 0 {
			return 0, fmt.Errorf("checks: invalid statement count")
		}
		count, e := strconv.Atoi(fields[2])
		if e != nil || count < 0 {
			return 0, fmt.Errorf("checks: invalid execution count")
		}
		total += n
		if count > 0 {
			covered += n
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("checks: report has no statements")
	}
	return 100 * float64(covered) / float64(total), nil
}
