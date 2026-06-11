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
	checkouts []string
	bases     int
	runs      []string
	runResult func(f Feature) (Summary, error)
	saves     int
}

func (d *fakeDeps) CheckoutFeatureBranch(branch, base string) error {
	d.checkouts = append(d.checkouts, branch)
	return nil
}
func (d *fakeDeps) CheckoutBase(base string) error { d.bases++; return nil }
func (d *fakeDeps) RunFeature(ctx context.Context, f Feature) (Summary, error) {
	d.runs = append(d.runs, f.ID)
	if d.runResult != nil {
		return d.runResult(f)
	}
	return Summary{TasksDone: 1}, nil
}
func (d *fakeDeps) SaveState(q *Queue) error { d.saves++; return nil }
func (d *fakeDeps) Logf(string, ...any)      {}

func TestEngineRun_DrainsQueue(t *testing.T) {
	q := &Queue{BaseBranch: "main", Features: ParseFeatures("## A\n\nbody\n\n## B\n\nbody\n")}
	d := &fakeDeps{}
	if err := Run(context.Background(), q, d); err != nil {
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
	if err := Run(context.Background(), q, d); err != nil {
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
	if err := Run(context.Background(), q, d); err != nil {
		t.Fatal(err)
	}
	if q.Features[0].Status != StatusFailed {
		t.Errorf("got %+v", q.Features[0])
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
	if err := Run(context.Background(), q, d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(order) != "[F002 F001]" {
		t.Errorf("order = %v, want F002 first (resume in_progress)", order)
	}
}
