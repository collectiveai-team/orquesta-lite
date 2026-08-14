package commands

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/collectiveai-team/orquesta-lite/internal/eventlog"
)

// LogViewOptions controls how `orq-lite log` renders the run.log timeline.
type LogViewOptions struct {
	// Role, when set, keeps only agent_run events for that role.
	Role string
	// Event, when set, keeps only events of that type.
	Event string
	// Expand, when > 0, prints that one event (by its 1-based timeline index)
	// in full and ignores the other filters.
	Expand int
	// Full prints the complete stdout/stderr/final_text tails under every
	// shown agent_run instead of just the one-line preview.
	Full bool
}

type logRecord struct {
	ts    string
	event eventlog.Event
}

// Log reads <projectDir>/.orquestalite/run.log (newline-delimited JSON written
// by eventlog) and renders a readable, filterable timeline to w. The same
// structured events that scroll past during a run can thus be reviewed and
// drilled into after the fact.
func Log(projectDir string, w io.Writer, opts LogViewOptions) error {
	path := filepath.Join(projectDir, ".orquestalite", "run.log")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(w, "no run.log yet (run `orq-lite plan` or `orq-lite run` first)")
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	records, err := decodeRunLog(f)
	if err != nil {
		return err
	}
	return renderRunLog(w, records, opts)
}

func decodeRunLog(r io.Reader) ([]logRecord, error) {
	var recs []logRecord
	sc := bufio.NewScanner(r)
	// Tails can be a couple of KB each; allow generously long lines.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			// Skip malformed lines rather than abort the whole view.
			continue
		}
		ts, _ := raw["ts"].(string)
		etype, _ := raw["event"].(string)
		delete(raw, "ts")
		delete(raw, "event")
		recs = append(recs, logRecord{ts: ts, event: eventlog.Event{Type: etype, Fields: raw}})
	}
	return recs, sc.Err()
}

func renderRunLog(w io.Writer, recs []logRecord, opts LogViewOptions) error {
	if opts.Expand > 0 {
		if opts.Expand > len(recs) {
			return fmt.Errorf("no event #%d (log has %d events)", opts.Expand, len(recs))
		}
		renderExpanded(w, opts.Expand, recs[opts.Expand-1])
		return nil
	}

	shown := 0
	for i, rec := range recs {
		if !matchesFilter(rec, opts) {
			continue
		}
		shown++
		renderCompact(w, i+1, rec, opts.Full)
	}
	if shown == 0 {
		fmt.Fprintln(w, "no matching events")
	}
	return nil
}

func matchesFilter(rec logRecord, opts LogViewOptions) bool {
	if opts.Event != "" && rec.event.Type != opts.Event {
		return false
	}
	if opts.Role != "" {
		// Only agent_run carries a role; a role filter therefore narrows to
		// that role's agent runs and drops everything else.
		if rec.event.Type != "agent_run" || fmt.Sprint(rec.event.Fields["role"]) != opts.Role {
			return false
		}
	}
	return true
}

func renderCompact(w io.Writer, idx int, rec logRecord, full bool) {
	fmt.Fprintf(w, "#%-3d [%s] %s\n", idx, tsClock(rec.ts), eventlog.HumanLine(rec.event))
	if full && rec.event.Type == "agent_run" {
		renderTails(w, rec.event.Fields)
	}
}

func renderExpanded(w io.Writer, idx int, rec logRecord) {
	fmt.Fprintf(w, "#%d [%s] %s\n", idx, tsClock(rec.ts), rec.event.Type)
	for _, k := range sortedKeys(rec.event.Fields) {
		s := fmt.Sprint(rec.event.Fields[k])
		if strings.Contains(s, "\n") {
			fmt.Fprintf(w, "  %s:\n", k)
			for _, line := range strings.Split(s, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			continue
		}
		fmt.Fprintf(w, "  %s: %s\n", k, s)
	}
}

func renderTails(w io.Writer, fields map[string]any) {
	for _, k := range []string{"final_text_tail", "stdout_tail", "stderr_tail"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" {
			continue
		}
		fmt.Fprintf(w, "    --- %s ---\n", k)
		for _, line := range strings.Split(s, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tsClock(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Local().Format("15:04:05")
	}
	if ts == "" {
		return "--:--:--"
	}
	return ts
}
