package factory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseFeatures_SplitsOnHeadings(t *testing.T) {
	md := `# Features backlog

intro text ignored

## Add login endpoint

POST /login with JWT.

## Add health check

GET /healthz returns 200.
`
	feats := ParseFeatures(md)
	if len(feats) != 2 {
		t.Fatalf("got %d features: %+v", len(feats), feats)
	}
	if feats[0].ID != "F001" || feats[0].Title != "Add login endpoint" {
		t.Errorf("f0 = %+v", feats[0])
	}
	if !strings.Contains(feats[0].Plan, "POST /login") {
		t.Errorf("f0 plan missing body: %q", feats[0].Plan)
	}
	if feats[1].Branch != "factory/002-add-health-check" {
		t.Errorf("f1 branch = %q", feats[1].Branch)
	}
	for _, f := range feats {
		if f.Status != StatusPending {
			t.Errorf("%s status = %q", f.ID, f.Status)
		}
	}
}

func TestParseFeatures_NoHeadingsSingleFeature(t *testing.T) {
	feats := ParseFeatures("Build a TODO API with SQLite storage.\nKeep it small.")
	if len(feats) != 1 {
		t.Fatalf("got %d features", len(feats))
	}
	if feats[0].Title != "Build a TODO API with SQLite storage." {
		t.Errorf("title = %q", feats[0].Title)
	}
}

func TestParseFeatures_EmptyInput(t *testing.T) {
	if feats := ParseFeatures("   \n\n"); feats != nil {
		t.Errorf("expected nil, got %+v", feats)
	}
}

func TestNewFeatures_AssignsMetadataAndDropsEmpty(t *testing.T) {
	feats := NewFeatures([]FeatureDraft{
		{Title: "Add login endpoint", Plan: "POST /login with JWT."},
		{Title: "ignored", Plan: "   "}, // empty plan -> dropped
		{Title: "", Plan: "GET /healthz returns 200."},
	})
	if len(feats) != 2 {
		t.Fatalf("got %d features: %+v", len(feats), feats)
	}
	if feats[0].ID != "F001" || feats[0].Status != StatusPending || feats[0].Branch != "factory/001-add-login-endpoint" {
		t.Errorf("f0 = %+v", feats[0])
	}
	// Empty title falls back to the plan's first non-empty line, and IDs stay
	// sequential (the dropped draft does not leave a gap).
	if feats[1].ID != "F002" || feats[1].Title != "GET /healthz returns 200." {
		t.Errorf("f1 = %+v", feats[1])
	}
}

func TestNewFeatures_CarriesVisualFlag(t *testing.T) {
	feats := NewFeatures([]FeatureDraft{
		{Title: "Capacity page", Plan: "render /capacity", Visual: true},
		{Title: "Rollup API", Plan: "GET /api/capacity", Visual: false},
	})
	if len(feats) != 2 {
		t.Fatalf("got %d features", len(feats))
	}
	if !feats[0].Visual {
		t.Error("F001 should carry Visual=true")
	}
	if feats[1].Visual {
		t.Error("F002 should carry Visual=false")
	}
}

func TestQueuePlannedFeatures(t *testing.T) {
	q := &Queue{} // nil PlannedFeatures map
	if q.FeatureIsPlanned("F001") {
		t.Fatal("nil map must report not planned (no panic)")
	}
	q.MarkFeaturePlanned("F001")
	if !q.FeatureIsPlanned("F001") {
		t.Error("F001 should be planned after MarkFeaturePlanned")
	}
	if q.FeatureIsPlanned("F002") {
		t.Error("F002 was never planned")
	}
}

func TestTasksFilePath(t *testing.T) {
	got := TasksFilePath("/proj", "F003")
	want := "/proj/.orquestalite/tasks-F003.json"
	if got != want {
		t.Errorf("TasksFilePath = %q, want %q", got, want)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Add login endpoint!":  "add-login-endpoint",
		"  Weird___chars///  ": "weird-chars",
		"":                     "feature",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueueStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody a\n\n## B\n\nbody b\n")}
	if err := Save(dir, q); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseBranch != "main" || len(got.Features) != 2 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

type fakeDeps struct {
	checkouts   []string
	bases       int
	runs        []string
	runResult   func(f Feature) (Summary, error)
	publish     func(f Feature) (string, error)
	saves       int
	checkpoints []string // feature IDs for which CheckpointResidue ran
	checkpoint  func(f Feature) (bool, error)
	reusePlans  map[string]bool // feature ID -> reusePlan passed to RunFeature
	merges      []string        // feature branches passed to MergeFeatureToBase
	mergeMethod string          // method to return ("" -> "ff")
	mergeErr    error           // when set, MergeFeatureToBase fails
	events      []string        // event names recorded via Event
}

func (d *fakeDeps) CheckoutFeatureBranch(branch, base string) error {
	d.checkouts = append(d.checkouts, branch)
	return nil
}
func (d *fakeDeps) CheckpointResidue(f Feature) (bool, error) {
	d.checkpoints = append(d.checkpoints, f.ID)
	if d.checkpoint != nil {
		return d.checkpoint(f)
	}
	return false, nil
}
func (d *fakeDeps) CheckoutBase(base string) error { d.bases++; return nil }
func (d *fakeDeps) RunFeature(ctx context.Context, f Feature, reusePlan bool) (Summary, error) {
	d.runs = append(d.runs, f.ID)
	if d.reusePlans == nil {
		d.reusePlans = map[string]bool{}
	}
	d.reusePlans[f.ID] = reusePlan
	if d.runResult != nil {
		return d.runResult(f)
	}
	return Summary{TasksDone: 1}, nil
}
func (d *fakeDeps) PublishFeature(ctx context.Context, f Feature, base string) (string, error) {
	if d.publish != nil {
		return d.publish(f)
	}
	return "", nil
}
func (d *fakeDeps) SaveState(q *Queue) error { d.saves++; return nil }
func (d *fakeDeps) Logf(string, ...any)      {}
func (d *fakeDeps) MergeFeatureToBase(branch, base string) (string, error) {
	d.merges = append(d.merges, branch)
	if d.mergeErr != nil {
		return "", d.mergeErr
	}
	if d.mergeMethod != "" {
		return d.mergeMethod, nil
	}
	return "ff", nil
}
func (d *fakeDeps) Event(name string, _ map[string]any) {
	d.events = append(d.events, name)
}

func TestEngineRun_DrainsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if len(d.runs) != 2 || d.bases != 2 {
		t.Errorf("runs=%v bases=%d", d.runs, d.bases)
	}
	for _, f := range q.Features {
		if f.Status != StatusDone {
			t.Errorf("%s = %q", f.ID, f.Status)
		}
		if f.StartedAt == nil || f.FinishedAt == nil {
			t.Errorf("%s missing timestamps", f.ID)
		}
	}
}

func TestEngineRun_FeatureFailureContinues(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{runResult: func(f Feature) (Summary, error) {
		if f.ID == "F001" {
			return Summary{}, errors.New("agents exploded")
		}
		return Summary{TasksDone: 2}, nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if q.Features[0].Status != StatusFailed || q.Features[0].Error == "" {
		t.Errorf("f0 = %+v", q.Features[0])
	}
	if q.Features[1].Status != StatusDone || q.Features[1].TasksDone != 2 {
		t.Errorf("f1 = %+v", q.Features[1])
	}
}

func TestEngineRun_AllTasksFailedMarksFeatureFailed(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{runResult: func(Feature) (Summary, error) {
		return Summary{TasksFailed: 3}, nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if q.Features[0].Status != StatusFailed {
		t.Errorf("got %+v", q.Features[0])
	}
}

func TestEngineRun_CheckpointsResidueBeforeReturningToBase(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	var checkpointedWhileDirty bool
	d := &fakeDeps{
		runResult: func(Feature) (Summary, error) {
			return Summary{}, fmt.Errorf("all agents failed for role tester")
		},
		checkpoint: func(f Feature) (bool, error) {
			checkpointedWhileDirty = true
			return true, nil
		},
	}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if !checkpointedWhileDirty {
		t.Error("expected CheckpointResidue to run for the failed feature")
	}
	// Checkpoint must happen before the base checkout so the tree is clean.
	if len(d.checkpoints) != 1 || d.bases != 1 {
		t.Errorf("checkpoints=%v bases=%d, want one checkpoint then one base checkout", d.checkpoints, d.bases)
	}
	if q.Features[0].Status != StatusFailed {
		t.Errorf("feature status = %q, want failed", q.Features[0].Status)
	}
}

func TestEngineRun_CheckpointErrorAbortsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{checkpoint: func(Feature) (bool, error) {
		return false, fmt.Errorf("commit failed")
	}}
	err := Run(context.Background(), q, Config{}, d)
	if err == nil || d.bases != 0 {
		t.Fatalf("err=%v bases=%d; a checkpoint failure must abort before checking out base", err, d.bases)
	}
}

func TestEngineRun_ResumeReusesPlanForOwnerAndRetriesFailed(t *testing.T) {
	// F001 failed earlier and was already decomposed (owns tasks-F001.json);
	// F002 is pending and has never been planned.
	q := &Queue{
		BaseBranch:      "main",
		Features:        ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n"),
		PlannedFeatures: map[string]bool{"F001": true},
	}
	q.Features[0].Status = StatusFailed
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{Resume: true}, d); err != nil {
		t.Fatal(err)
	}
	// Both features run: the failed one (retried) and the pending one.
	if fmt.Sprint(d.runs) != "[F001 F002]" {
		t.Fatalf("runs = %v, want [F001 F002]", d.runs)
	}
	// F001 owns tasks.json -> reuse its plan; F002 is fresh -> re-plan.
	if !d.reusePlans["F001"] {
		t.Errorf("F001 should reuse its existing task list on resume")
	}
	if d.reusePlans["F002"] {
		t.Errorf("F002 (never planned) must not reuse a plan")
	}
}

func TestEngineRun_ReplanForcesFreshPlanEvenWhenPlanned(t *testing.T) {
	// F001 was already decomposed, but --replan must discard it and re-plan.
	q := &Queue{
		BaseBranch:      "main",
		Features:        ParseFeatures("## A\n\nbody\n"),
		PlannedFeatures: map[string]bool{"F001": true},
	}
	q.Features[0].Status = StatusFailed
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{Resume: true, Replan: true}, d); err != nil {
		t.Fatal(err)
	}
	if d.reusePlans["F001"] {
		t.Errorf("--replan must force a fresh decomposition, not reuse F001's task list")
	}
}

func TestEngineRun_ReusesPlanByDefaultForInterruptedFeature(t *testing.T) {
	// An interrupted (in_progress) feature was already planned; a plain run (no
	// --resume) must reuse its task list rather than re-plan from scratch.
	q := &Queue{
		BaseBranch:      "main",
		Features:        ParseFeatures("## A\n\nbody\n"),
		PlannedFeatures: map[string]bool{"F001": true},
	}
	q.Features[0].Status = StatusInProgress
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if !d.reusePlans["F001"] {
		t.Errorf("interrupted, already-planned F001 should reuse its task list by default")
	}
}

func TestEngineRun_WithoutResumeSkipsFailedFeature(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	q.Features[0].Status = StatusFailed
	d := &fakeDeps{}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.runs) != "[F002]" {
		t.Fatalf("runs = %v, want only [F002] (failed F001 is terminal without --resume)", d.runs)
	}
}

func TestEngineRun_ResumeDoesNotLoopOnRepeatedFailure(t *testing.T) {
	// A feature that keeps failing must be attempted once and then passed over,
	// not re-selected forever.
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	q.Features[0].Status = StatusFailed
	d := &fakeDeps{runResult: func(f Feature) (Summary, error) {
		if f.ID == "F001" {
			return Summary{}, errors.New("still broken")
		}
		return Summary{TasksDone: 1}, nil
	}}
	if err := Run(context.Background(), q, Config{Resume: true}, d); err != nil {
		t.Fatal(err)
	}
	// F001 attempted exactly once despite re-failing; F002 still gets its turn.
	if fmt.Sprint(d.runs) != "[F001 F002]" {
		t.Fatalf("runs = %v, want [F001 F002] (no infinite retry of F001)", d.runs)
	}
}

func TestEngineRun_ResumesInProgressFirst(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	q.Features[1].Status = StatusInProgress // interrupted earlier
	var order []string
	d := &fakeDeps{runResult: func(f Feature) (Summary, error) {
		order = append(order, f.ID)
		return Summary{TasksDone: 1}, nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(order) != "[F002 F001]" {
		t.Errorf("order = %v, want F002 first (resume in_progress)", order)
	}
}

func TestEngineRun_BudgetStopsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n\n## C\n\nbody\n")}
	d := &fakeDeps{runResult: func(Feature) (Summary, error) {
		return Summary{TasksDone: 1, CostUSD: 6.0}, nil
	}}
	if err := Run(context.Background(), q, Config{BudgetUSD: 10}, d); err != nil {
		t.Fatal(err)
	}
	// F001 spends $6 (under budget, runs), F002 spends $6 (total $12 >= $10
	// checked before F003) — so exactly two features run.
	if len(d.runs) != 2 {
		t.Fatalf("runs = %v, want 2 features before budget stop", d.runs)
	}
	if q.Features[2].Status != StatusPending {
		t.Errorf("F003 = %q, want pending (resumable)", q.Features[2].Status)
	}
	if got := SpentUSD(q); got != 12.0 {
		t.Errorf("SpentUSD = %v", got)
	}
}

func TestEngineRun_PublishesDoneFeatures(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n")}
	d := &fakeDeps{publish: func(f Feature) (string, error) {
		return "https://github.com/x/y/pull/1", nil
	}}
	if err := Run(context.Background(), q, Config{}, d); err != nil {
		t.Fatal(err)
	}
	if q.Features[0].PRURL != "https://github.com/x/y/pull/1" {
		t.Errorf("PRURL = %q", q.Features[0].PRURL)
	}
}
