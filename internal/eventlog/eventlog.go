package eventlog

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const DefaultRotateBytes = 50 * 1024 * 1024

// Format controls the layout of pretty (stdout) lines. The JSONL file written
// to disk is unaffected — it always contains the full structured event.
type Format string

const (
	// FormatVerbose is the historical default: one line per event with every
	// field as key=value. Useful for tooling that parses stdout.
	FormatVerbose Format = "verbose"
	// FormatHuman emits compact lines per event ("[time] coder agent=X → done
	// (12s)") so an operator following a long run can read it without
	// drowning in truncated stdout/stderr tails.
	FormatHuman Format = "human"
)

type Event struct {
	Type   string
	Fields map[string]any
}

type Logger struct {
	mu          sync.Mutex
	path        string
	pretty      io.Writer
	f           *os.File
	RotateBytes int64
	format      Format
}

func Open(path string, pretty io.Writer) (*Logger, error) {
	return OpenWithFormat(path, pretty, FormatVerbose)
}

// OpenWithFormat is like Open but lets callers pick the stdout pretty format.
// The JSONL file content is identical across formats.
func OpenWithFormat(path string, pretty io.Writer, format Format) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if format == "" {
		format = FormatVerbose
	}
	return &Logger{path: path, pretty: pretty, f: f, RotateBytes: DefaultRotateBytes, format: format}, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *Logger) Log(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	rec := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "event": e.Type}
	for k, v := range e.Fields {
		rec[k] = v
	}
	raw, _ := json.Marshal(rec)
	raw = append(raw, '\n')
	_, _ = l.f.Write(raw)
	if l.format == FormatHuman {
		_, _ = fmt.Fprintf(l.pretty, "[%s] %s\n", time.Now().Format("15:04:05"), humanLine(e))
	} else {
		_, _ = fmt.Fprintf(l.pretty, "[%s] %s %s\n", time.Now().Format("15:04:05"), e.Type, summariseFields(e.Fields))
	}

	if info, err := l.f.Stat(); err == nil && info.Size() >= l.RotateBytes {
		l.rotateLocked()
	}
}

func (l *Logger) rotateLocked() {
	_ = l.f.Close()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	gzPath := filepath.Join(filepath.Dir(l.path), fmt.Sprintf("run-%s.log.gz", stamp))
	src, err := os.Open(l.path)
	if err != nil {
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		return
	}
	defer src.Close()
	dst, err := os.Create(gzPath)
	if err != nil {
		l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		return
	}
	gz := gzip.NewWriter(dst)
	_, _ = io.Copy(gz, src)
	_ = gz.Close()
	_ = dst.Close()
	_ = os.Truncate(l.path, 0)
	l.f, _ = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// humanLine renders an event into a one-line operator-friendly summary. Falls
// back to a compact `type {key=val}` form for events the formatter doesn't
// recognise so additions to event vocabulary don't silently disappear.
func humanLine(e Event) string {
	switch e.Type {
	case "agent_run":
		return formatAgentRun(e.Fields)
	case "task_done":
		return fmt.Sprintf("task_done task=%v commit=%v",
			fs(e.Fields["task_id"]), shortSHA(fs(e.Fields["commit_sha"])))
	case "task_done_no_commit":
		return fmt.Sprintf("task_done task=%v (commit skipped: %v)",
			fs(e.Fields["task_id"]), fs(e.Fields["reason"]))
	case "plan_written":
		return fmt.Sprintf("plan_written tasks=%v path=%v",
			fs(e.Fields["tasks_count"]), fs(e.Fields["path"]))
	case "preflight_skipped_agent":
		return fmt.Sprintf("preflight: agent %v skipped (%v: %v)",
			fs(e.Fields["agent"]), fs(e.Fields["reason"]), fs(e.Fields["binary"]))
	case "agent_marked_skipped":
		return fmt.Sprintf("agent_marked_skipped %v role=%v after %v failures",
			fs(e.Fields["agent"]), fs(e.Fields["role"]), fs(e.Fields["threshold"]))
	case "handoff_written":
		return fmt.Sprintf("handoff task=%v path=%v",
			fs(e.Fields["task_id"]), fs(e.Fields["path"]))
	case "full_suite_failed":
		return "full_suite_failed (see run.log for details)"
	}
	return fmt.Sprintf("%s %s", e.Type, summariseFields(e.Fields))
}

func formatAgentRun(fs_ map[string]any) string {
	role := fs(fs_["role"])
	agent := fs(fs_["agent"])
	dur := fs(fs_["duration_s"])
	reason := fs(fs_["fallback_reason"])
	outcome := "ok"
	switch {
	case reason == "rate_limit":
		outcome = "rate-limited"
	case reason == "agent_crashed":
		outcome = "crashed"
	case reason == "result_missing":
		outcome = "no-result"
	case reason != "":
		outcome = reason
	}
	return fmt.Sprintf("%s/%s → %s (%ss)", role, agent, outcome, dur)
}

func fs(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func summariseFields(fs map[string]any) string {
	parts := make([]string, 0, len(fs))
	for k, v := range fs {
		if k == "result_snapshot" {
			parts = append(parts, k+"=...")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, summariseValue(v)))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

func summariseValue(v any) string {
	s := fmt.Sprint(v)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return truncateUTF8(s, 300)
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	raw := s[:limit]
	for !utf8.ValidString(raw) && len(raw) > 0 {
		raw = raw[:len(raw)-1]
	}
	return raw + "..."
}
