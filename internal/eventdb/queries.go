package eventdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/lionelchamorro/orquestalite/internal/cost"
)

// RunSummary is the contract shape served by GET /api/runs (features.md /
// orquesta docs/orq-lite-query-api.md). Do not rename fields.
type RunSummary struct {
	RunID        string   `json:"run_id"`
	Command      string   `json:"command"`
	Args         []string `json:"args"`
	Status       string   `json:"status"`
	StartedAt    string   `json:"started_at"`
	FinishedAt   *string  `json:"finished_at"`
	DurationS    *float64 `json:"duration_s"`
	OrqVersion   string   `json:"orq_version"`
	CostUSD      float64  `json:"cost_usd"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	AgentRuns    int      `json:"agent_runs"`
	TasksDone    int      `json:"tasks_done"`
	TasksFailed  int      `json:"tasks_failed"`
}

// AgentRunRecord is the contract shape served by GET /api/agent-runs.
type AgentRunRecord struct {
	Ts                string  `json:"ts"`
	RunID             string  `json:"run_id"`
	Role              string  `json:"role"`
	Agent             string  `json:"agent"`
	TaskID            string  `json:"task_id"`
	Cycle             int     `json:"cycle"`
	Attempt           int     `json:"attempt"`
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	DurationS         float64 `json:"duration_s"`
	ExitCode          int     `json:"exit_code"`
	TimedOut          bool    `json:"timed_out"`
	RateLimited       bool    `json:"rate_limited"`
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CachedInputTokens int     `json:"cached_input_tokens"`
	ReasoningTokens   int     `json:"reasoning_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	ArtifactsDir      string  `json:"artifacts_dir"`
}

// CostRow is one row of GET /api/stats/cost.
type CostRow struct {
	Key          string  `json:"key"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	AgentRuns    int     `json:"agent_runs"`
}

type RunsFilter struct {
	Active *bool // nil = all; true = status running; false = not running
}

type AgentRunsFilter struct {
	RunID  string
	TaskID string
	Role   string
	Agent  string
}

// Runs lists runs newest-first with cost/token/task aggregates.
func (d *DB) Runs(f RunsFilter, limit, offset int) ([]RunSummary, int, error) {
	where, args := "", []any{}
	if f.Active != nil {
		if *f.Active {
			where = "WHERE status = 'running'"
		} else {
			where = "WHERE status != 'running'"
		}
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query(`SELECT run_id, command, args, status, started_at, finished_at, duration_s, orq_version
		FROM runs `+where+` ORDER BY started_at DESC, run_id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []RunSummary{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := d.decorateRuns(out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Run returns one run's summary, or (nil, nil) when the id is unknown.
func (d *DB) Run(id string) (*RunSummary, error) {
	row := d.sql.QueryRow(`SELECT run_id, command, args, status, started_at, finished_at, duration_s, orq_version
		FROM runs WHERE run_id = ?`, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rs := []RunSummary{r}
	if err := d.decorateRuns(rs); err != nil {
		return nil, err
	}
	return &rs[0], nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(row rowScanner) (RunSummary, error) {
	var r RunSummary
	var argsJSON string
	var finished sql.NullString
	var dur sql.NullFloat64
	if err := row.Scan(&r.RunID, &r.Command, &argsJSON, &r.Status, &r.StartedAt, &finished, &dur, &r.OrqVersion); err != nil {
		return r, err
	}
	r.Args = []string{}
	_ = json.Unmarshal([]byte(argsJSON), &r.Args)
	if r.Args == nil {
		r.Args = []string{}
	}
	if finished.Valid {
		r.FinishedAt = &finished.String
	}
	if dur.Valid {
		r.DurationS = &dur.Float64
	}
	return r, nil
}

// decorateRuns fills cost/token/agent-run/task aggregates for a page of runs
// with two grouped queries (no per-run N+1).
func (d *DB) decorateRuns(rs []RunSummary) error {
	if len(rs) == 0 {
		return nil
	}
	idx := make(map[string]*RunSummary, len(rs))
	placeholders := make([]string, 0, len(rs))
	ids := make([]any, 0, len(rs))
	for i := range rs {
		idx[rs[i].RunID] = &rs[i]
		placeholders = append(placeholders, "?")
		ids = append(ids, rs[i].RunID)
	}
	in := strings.Join(placeholders, ",")

	rows, err := d.sql.Query(`SELECT run_id, model, SUM(input_tokens), SUM(output_tokens), COUNT(*)
		FROM agent_runs WHERE run_id IN (`+in+`) GROUP BY run_id, model`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var runID, model string
		var input, output, n int
		if err := rows.Scan(&runID, &model, &input, &output, &n); err != nil {
			return err
		}
		r := idx[runID]
		r.InputTokens += input
		r.OutputTokens += output
		r.AgentRuns += n
		if usd, ok := cost.EstimateUSD(model, input, output); ok {
			r.CostUSD += usd
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	trows, err := d.sql.Query(`SELECT run_id, type, COUNT(*)
		FROM events WHERE run_id IN (`+in+`)
		AND type IN ('task_done', 'task_done_no_commit', 'task_failed')
		GROUP BY run_id, type`, ids...)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var runID, typ string
		var n int
		if err := trows.Scan(&runID, &typ, &n); err != nil {
			return err
		}
		r := idx[runID]
		if typ == "task_failed" {
			r.TasksFailed += n
		} else {
			r.TasksDone += n
		}
	}
	return trows.Err()
}

// Events returns a run's raw events in log order, optionally filtered.
func (d *DB) Events(runID, typ, taskID string, limit, offset int) ([]json.RawMessage, int, error) {
	where, args := "WHERE run_id = ?", []any{runID}
	if typ != "" {
		where += " AND type = ?"
		args = append(args, typ)
	}
	if taskID != "" {
		where += " AND task_id = ?"
		args = append(args, taskID)
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM events "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query("SELECT raw FROM events "+where+" ORDER BY id LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, total, rows.Err()
}

// AgentRuns lists agent invocations newest-first with per-run cost.
func (d *DB) AgentRuns(f AgentRunsFilter, limit, offset int) ([]AgentRunRecord, int, error) {
	where, args := "WHERE 1=1", []any{}
	for col, v := range map[string]string{"run_id": f.RunID, "task_id": f.TaskID, "role": f.Role, "agent": f.Agent} {
		if v != "" {
			where += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	var total int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM agent_runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.sql.Query(`SELECT ts, run_id, role, agent, task_id, cycle, attempt, provider, model,
			duration_s, exit_code, timed_out, rate_limited,
			input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, artifacts_dir
		FROM agent_runs `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AgentRunRecord{}
	for rows.Next() {
		var r AgentRunRecord
		var timedOut, rateLimited int
		if err := rows.Scan(&r.Ts, &r.RunID, &r.Role, &r.Agent, &r.TaskID, &r.Cycle, &r.Attempt,
			&r.Provider, &r.Model, &r.DurationS, &r.ExitCode, &timedOut, &rateLimited,
			&r.InputTokens, &r.OutputTokens, &r.CachedInputTokens, &r.ReasoningTokens, &r.ArtifactsDir); err != nil {
			return nil, 0, err
		}
		r.TimedOut = timedOut != 0
		r.RateLimited = rateLimited != 0
		if usd, ok := cost.EstimateUSD(r.Model, r.InputTokens, r.OutputTokens); ok {
			r.CostUSD = usd
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// costStatsColumns maps the by= parameter to a grouping column. Fixed map —
// never interpolate request input into SQL.
var costStatsColumns = map[string]string{
	"run":   "run_id",
	"agent": "agent",
	"task":  "task_id",
	"role":  "role",
}

// CostStats aggregates cost/tokens grouped by run|agent|task|role, sorted by
// cost_usd descending (key ascending on ties, for stable output).
func (d *DB) CostStats(by string) ([]CostRow, error) {
	col, ok := costStatsColumns[by]
	if !ok {
		col = "run_id"
	}
	rows, err := d.sql.Query(`SELECT ` + col + `, model, SUM(input_tokens), SUM(output_tokens), COUNT(*)
		FROM agent_runs GROUP BY ` + col + `, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	acc := map[string]*CostRow{}
	for rows.Next() {
		var key, model string
		var input, output, n int
		if err := rows.Scan(&key, &model, &input, &output, &n); err != nil {
			return nil, err
		}
		row := acc[key]
		if row == nil {
			row = &CostRow{Key: key}
			acc[key] = row
		}
		row.InputTokens += input
		row.OutputTokens += output
		row.AgentRuns += n
		if usd, ok := cost.EstimateUSD(model, input, output); ok {
			row.CostUSD += usd
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CostRow, 0, len(acc))
	for _, r := range acc {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}
