// Package cost rolls up token spend per task and per run by joining the
// orchestrator's own run.log (which records the session_id of every agent
// invocation) against agtop's per-session cost analysis. agtop
// (https://github.com/...) already discovers each agent CLI's local session
// files and prices them against live pricing tables, so orq-lite delegates
// instead of re-implementing pricing.
package cost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

// ErrAgtopUnavailable is returned when the agtop binary is not on PATH.
// Cost tracking is an optional capability: callers degrade gracefully.
var ErrAgtopUnavailable = errors.New("cost tracking unavailable: agtop not found on PATH (https://github.com/raine/agtop)")

// Session is the slice of agtop's per-session analysis that orq-lite uses.
type Session struct {
	SessionID string  `json:"session_id"`
	Client    string  `json:"client"`
	Model     string  `json:"model"`
	Cwd       string  `json:"cwd"`
	StartedAt string  `json:"started_at"`
	Tokens    Tokens  `json:"tokens"`
	Cost      Money   `json:"cost"`
	Duration  float64 `json:"duration_secs"`
}

type Tokens struct {
	Input       int `json:"input"`
	CachedInput int `json:"cached_input"`
	Output      int `json:"output"`
}

type Money struct {
	Total float64 `json:"total"`
}

type agtopOutput struct {
	Sessions []Session `json:"sessions"`
}

// Collect runs agtop and returns its sessions indexed by session ID.
func Collect(ctx context.Context) (map[string]Session, error) {
	if _, err := exec.LookPath("agtop"); err != nil {
		return nil, ErrAgtopUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "agtop", "--json", "--no-update-check").Output()
	if err != nil {
		return nil, fmt.Errorf("agtop --json: %w", err)
	}
	var parsed agtopOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse agtop output: %w", err)
	}
	byID := make(map[string]Session, len(parsed.Sessions))
	for _, s := range parsed.Sessions {
		byID[s.SessionID] = s
	}
	return byID, nil
}

// AgentRun is one agent invocation extracted from run.log.
type AgentRun struct {
	TS        time.Time
	TaskID    string
	Role      string
	Agent     string
	SessionID string
}

// RunsFromLog extracts agent_run events (with a session id) from a run.log
// JSONL file. Missing files yield an empty slice.
func RunsFromLog(path string) ([]AgentRun, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var runs []AgentRun
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev struct {
			Event     string `json:"event"`
			TS        string `json:"ts"`
			TaskID    string `json:"task_id"`
			Role      string `json:"role"`
			Agent     string `json:"agent"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Event != "agent_run" || ev.SessionID == "" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, ev.TS)
		runs = append(runs, AgentRun{TS: ts, TaskID: ev.TaskID, Role: ev.Role, Agent: ev.Agent, SessionID: ev.SessionID})
	}
	return runs, sc.Err()
}

// TaskCost aggregates spend for one task (or synthetic id like _plan/_review).
type TaskCost struct {
	TaskID    string
	Runs      int
	Priced    int // runs whose session agtop could price
	TotalUSD  float64
	InputTok  int
	OutputTok int
}

// Report is the joined rollup.
type Report struct {
	Tasks    []TaskCost
	TotalUSD float64
	Runs     int
	Priced   int
}

// Rollup joins agent runs against agtop sessions. Sessions agtop does not
// know (expired logs, unsupported client) count as runs but not cost.
func Rollup(runs []AgentRun, sessions map[string]Session) Report {
	byTask := map[string]*TaskCost{}
	var order []string
	for _, r := range runs {
		id := r.TaskID
		if id == "" {
			id = "(none)"
		}
		tc, ok := byTask[id]
		if !ok {
			tc = &TaskCost{TaskID: id}
			byTask[id] = tc
			order = append(order, id)
		}
		tc.Runs++
		if s, ok := sessions[r.SessionID]; ok {
			tc.Priced++
			tc.TotalUSD += s.Cost.Total
			tc.InputTok += s.Tokens.Input + s.Tokens.CachedInput
			tc.OutputTok += s.Tokens.Output
		}
	}

	sort.Strings(order)
	rep := Report{}
	for _, id := range order {
		tc := byTask[id]
		rep.Tasks = append(rep.Tasks, *tc)
		rep.TotalUSD += tc.TotalUSD
		rep.Runs += tc.Runs
		rep.Priced += tc.Priced
	}
	return rep
}

// SpendSince sums the cost of sessions referenced by agent runs at or after t.
// Used for per-feature attribution in factory mode (run.log is shared across
// features; the time window isolates one feature's spend).
func SpendSince(runs []AgentRun, sessions map[string]Session, t time.Time) float64 {
	seen := map[string]bool{}
	total := 0.0
	for _, r := range runs {
		if r.TS.Before(t) || seen[r.SessionID] {
			continue
		}
		seen[r.SessionID] = true
		if s, ok := sessions[r.SessionID]; ok {
			total += s.Cost.Total
		}
	}
	return total
}
