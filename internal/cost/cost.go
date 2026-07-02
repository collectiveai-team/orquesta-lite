// Package cost rolls up token spend per task and per run from the
// orchestrator's own run.log. agtop remains an optional enrichment source for
// precise per-session USD, with embedded prices used as a fallback estimate
// when first-party token counts are present.
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
var ErrAgtopUnavailable = errors.New("cost tracking unavailable: agtop not found on PATH")

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
	Provider  string
	Model     string
	SessionID string
	InputTok  int
	OutputTok int
}

// RunsFromLog extracts agent_run events from a run.log JSONL file. Missing
// files yield an empty slice.
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
			Event        string `json:"event"`
			TS           string `json:"ts"`
			TaskID       string `json:"task_id"`
			Role         string `json:"role"`
			Agent        string `json:"agent"`
			Provider     string `json:"provider"`
			Model        string `json:"model"`
			SessionID    string `json:"session_id"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Event != "agent_run" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, ev.TS)
		runs = append(runs, AgentRun{
			TS:        ts,
			TaskID:    ev.TaskID,
			Role:      ev.Role,
			Agent:     ev.Agent,
			Provider:  ev.Provider,
			Model:     ev.Model,
			SessionID: ev.SessionID,
			InputTok:  ev.InputTokens,
			OutputTok: ev.OutputTokens,
		})
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

// Rollup aggregates first-party token counts from agent runs, falling back to
// agtop token counts for older logs. USD uses agtop session costs first and
// embedded model prices second; unpriced runs still count toward Runs.
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
		input, output := r.InputTok, r.OutputTok
		if input == 0 && output == 0 {
			if s, ok := sessions[r.SessionID]; ok {
				input = s.Tokens.Input + s.Tokens.CachedInput
				output = s.Tokens.Output
			}
		}
		tc.InputTok += input
		tc.OutputTok += output

		if s, ok := sessions[r.SessionID]; ok {
			tc.Priced++
			tc.TotalUSD += s.Cost.Total
			continue
		}
		if usd, ok := estimateUSD(r.Model, input, output); ok {
			tc.Priced++
			tc.TotalUSD += usd
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
