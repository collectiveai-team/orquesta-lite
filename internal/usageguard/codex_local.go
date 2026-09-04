package usageguard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodexLocalReader recovers Codex usage from the rollout files the CLI already
// writes, without launching a process or touching the network. Codex records
// the account's rate limits alongside every token count, so the newest session
// file carries a reading no older than the last turn that ran.
//
// This is the same local-bookkeeping route menu-bar monitors use. It is offered
// as a fallback rather than the primary source because it reports whatever the
// last run observed: the reading carries its own timestamp so the guard can
// reject it once it has aged out.
type CodexLocalReader struct {
	// Root is a test seam; empty resolves CODEX_HOME or ~/.codex.
	Root string
}

func (r CodexLocalReader) Fetch(_ context.Context, env []string) (Snapshot, error) {
	root := r.Root
	if root == "" {
		if root = envValue(env, "CODEX_HOME"); root == "" {
			home := envValue(env, "HOME")
			if home == "" {
				var err error
				if home, err = os.UserHomeDir(); err != nil {
					return nil, fmt.Errorf("locate Codex home: %w", err)
				}
			}
			root = filepath.Join(home, ".codex")
		}
	}
	sessions, err := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(sessions) == 0 {
		return nil, fmt.Errorf("no Codex session files under %s", root)
	}
	// Newest first: only the most recent run can hold a current reading.
	sort.Slice(sessions, func(i, j int) bool {
		return sessionModTime(sessions[i]).After(sessionModTime(sessions[j]))
	})
	var lastErr error
	for _, path := range sessions[:min(len(sessions), 8)] {
		snapshot, err := parseCodexRolloutFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		return snapshot, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no rate limit record found")
	}
	return nil, fmt.Errorf("read Codex session usage: %w", lastErr)
}

func sessionModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// parseCodexRolloutFile returns the last rate limit record in one session file.
// Later records supersede earlier ones, so the file is read to the end.
func parseCodexRolloutFile(path string) (Snapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	var latest []byte
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"rate_limits"`) {
			latest = append(latest[:0], scanner.Bytes()...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, fmt.Errorf("%s has no rate limit record", filepath.Base(path))
	}
	return parseCodexRolloutRecord(latest)
}

// parseCodexRolloutRecord decodes one rollout event. Note the field names
// differ from the app-server RPC: the session log uses snake_case
// (used_percent, window_minutes, resets_at) where the RPC uses camelCase.
func parseCodexRolloutRecord(raw []byte) (Snapshot, error) {
	var record struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			RateLimits *struct {
				Primary   *codexRolloutWindow `json:"primary"`
				Secondary *codexRolloutWindow `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode Codex session record: %w", err)
	}
	if record.Payload.RateLimits == nil {
		return nil, fmt.Errorf("Codex session record carries no rate limits")
	}
	observedAt, err := time.Parse(time.RFC3339, record.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("decode Codex session timestamp: %w", err)
	}
	out := Snapshot{}
	add := func(limit *codexRolloutWindow) {
		if limit == nil {
			return
		}
		var window string
		switch limit.WindowMinutes {
		case 300:
			window = WindowFiveHour
		case 10080:
			window = WindowSevenDay
		default:
			return
		}
		used, err := Used(limit.UsedPercent)
		if err != nil {
			return
		}
		var resetsAt time.Time
		if limit.ResetsAt > 0 {
			resetsAt = time.Unix(limit.ResetsAt, 0)
		}
		out[window] = Window{UsedPercent: used, ResetsAt: resetsAt, ObservedAt: observedAt}
	}
	add(record.Payload.RateLimits.Primary)
	add(record.Payload.RateLimits.Secondary)
	if len(out) == 0 {
		return nil, fmt.Errorf("Codex session record has no 5h or 7d window")
	}
	return out, nil
}

type codexRolloutWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}
