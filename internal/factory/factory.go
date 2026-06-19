// Package factory turns orquestalite into a feature factory: a queue of
// features parsed from a single markdown file, each developed on its own git
// branch by a full plan+run cycle, processed sequentially until the queue is
// drained. State persists in .orquestalite/factory.json so an interrupted
// queue resumes where it left off.
package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
)

// Feature is one queued unit of work: a feature description that becomes a
// plan, a branch, and a full review-loop run.
type Feature struct {
	ID         string     `json:"id"` // "F001"
	Title      string     `json:"title"`
	Plan       string     `json:"plan"` // full markdown section fed to the parser
	Branch     string     `json:"branch"`
	Status     Status     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Task counts copied from tasks.json after the run finishes.
	TasksDone   int    `json:"tasks_done"`
	TasksFailed int    `json:"tasks_failed"`
	TasksOther  int    `json:"tasks_other"`
	Error       string `json:"error,omitempty"`
	// CostUSD is the agent spend attributed to this feature (sessions whose
	// agent runs started inside the feature's time window, priced via agtop).
	// Zero when cost tracking is unavailable.
	CostUSD float64 `json:"cost_usd,omitempty"`
	// PRURL is the pull request created for the feature branch (--pr).
	PRURL string `json:"pr_url,omitempty"`
}

type Queue struct {
	BaseBranch string    `json:"base_branch"`
	Features   []Feature `json:"features"`
	// PlannedFeatures records every feature whose task list has been decomposed
	// at least once (its tasks-<ID>.json exists). Reuse is the default: a feature
	// in this set is NOT re-planned on a retry/resume — its persisted task list
	// is continued so completed tasks are skipped. Only --replan forces a fresh
	// decomposition. A nil map means "nothing planned yet" (FeatureIsPlanned is
	// nil-safe), which is also what an old factory.json without this field loads
	// as — those features fall back to a fresh plan, the safe default.
	PlannedFeatures map[string]bool `json:"planned_features,omitempty"`
}

// FeatureIsPlanned reports whether feature id has already been decomposed (its
// tasks-<id>.json was written at least once). Nil-safe.
func (q *Queue) FeatureIsPlanned(id string) bool {
	return q.PlannedFeatures[id]
}

// MarkFeaturePlanned records that feature id now owns a persisted task list.
// Lazily initialises the map.
func (q *Queue) MarkFeaturePlanned(id string) {
	if q.PlannedFeatures == nil {
		q.PlannedFeatures = map[string]bool{}
	}
	q.PlannedFeatures[id] = true
}

// TasksFilePath is the per-feature task list path: .orquestalite/tasks-<id>.json.
// Each feature persists its decomposition here so retries reuse it instead of
// re-planning. The canonical .orquestalite/tasks.json is a scratch working copy
// of the feature currently running.
func TasksFilePath(projectDir, featureID string) string {
	return filepath.Join(projectDir, ".orquestalite", "tasks-"+featureID+".json")
}

// NextRunnable returns the first feature that still needs work, in queue order.
// An in_progress feature (interrupted run) always takes priority. Pending
// features are runnable; failed features are too only when resumeFailed is set
// (the --resume path retries them). Features whose ID is in skip are passed
// over — the engine uses this to avoid re-picking a feature it already
// attempted this run (so a feature that keeps failing can't loop forever).
func (q *Queue) NextRunnable(resumeFailed bool, skip map[string]bool) *Feature {
	for i := range q.Features {
		if skip[q.Features[i].ID] {
			continue
		}
		if q.Features[i].Status == StatusInProgress {
			return &q.Features[i]
		}
	}
	for i := range q.Features {
		if skip[q.Features[i].ID] {
			continue
		}
		s := q.Features[i].Status
		if s == StatusPending || (resumeFailed && s == StatusFailed) {
			return &q.Features[i]
		}
	}
	return nil
}

func StatePath(projectDir string) string {
	return filepath.Join(projectDir, ".orquestalite", "factory.json")
}

func Load(projectDir string) (*Queue, error) {
	raw, err := os.ReadFile(StatePath(projectDir))
	if err != nil {
		return nil, err
	}
	var q Queue
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, fmt.Errorf("parse factory.json: %w", err)
	}
	return &q, nil
}

func Save(projectDir string, q *Queue) error {
	path := StatePath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// FeatureDraft is a title+plan pair before queue metadata (ID, branch, status)
// is assigned. Both the heading-split fallback (ParseFeatures) and the LLM
// planner produce drafts, then hand them to NewFeatures for uniform numbering.
type FeatureDraft struct {
	Title string
	Plan  string
}

// NewFeatures turns drafts into queued features, assigning sequential IDs
// (F001, F002, ...), pending status, and a branch name. Drafts with an empty
// plan are dropped; an empty title falls back to the plan's first non-empty line.
func NewFeatures(drafts []FeatureDraft) []Feature {
	feats := make([]Feature, 0, len(drafts))
	for _, d := range drafts {
		plan := strings.TrimSpace(d.Plan)
		if plan == "" {
			continue
		}
		title := strings.TrimSpace(d.Title)
		if title == "" {
			title = firstNonEmptyLine(plan)
		}
		feats = append(feats, Feature{Title: title, Plan: plan})
	}
	for i := range feats {
		feats[i].ID = fmt.Sprintf("F%03d", i+1)
		feats[i].Status = StatusPending
		feats[i].Branch = fmt.Sprintf("factory/%03d-%s", i+1, Slug(feats[i].Title))
	}
	return feats
}

// ParseFeatures splits a markdown features file into one Feature per level-2
// heading ("## Title"). Text before the first heading is ignored (treated as
// preamble). A file without any level-2 heading becomes a single feature
// titled after its first non-empty line.
//
// This is the heading-split fallback. The factory's primary path is the LLM
// planner (vertical-slice extraction); ParseFeatures is retained for the
// offline/test path and as a structural baseline.
func ParseFeatures(markdown string) []Feature {
	lines := strings.Split(markdown, "\n")
	var drafts []FeatureDraft
	var cur *FeatureDraft

	flush := func() {
		if cur != nil {
			cur.Plan = strings.TrimSpace(cur.Plan)
			if cur.Plan != "" {
				drafts = append(drafts, *cur)
			}
			cur = nil
		}
	}

	for _, line := range lines {
		if title, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			cur = &FeatureDraft{Title: strings.TrimSpace(title), Plan: line + "\n"}
			continue
		}
		if cur != nil {
			cur.Plan += line + "\n"
		}
	}
	flush()

	if len(drafts) == 0 {
		body := strings.TrimSpace(markdown)
		if body == "" {
			return nil
		}
		drafts = []FeatureDraft{{Title: firstNonEmptyLine(body), Plan: body}}
	}

	return NewFeatures(drafts)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "# "))
		if line != "" {
			return line
		}
	}
	return "feature"
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug converts a title to a short branch-safe identifier.
func Slug(title string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "feature"
	}
	return s
}
