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
}

func Open(path string, pretty io.Writer) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{path: path, pretty: pretty, f: f, RotateBytes: DefaultRotateBytes}, nil
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
	_, _ = fmt.Fprintf(l.pretty, "[%s] %s %s\n", time.Now().Format("15:04:05"), e.Type, summariseFields(e.Fields))

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
