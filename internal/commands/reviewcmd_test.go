package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/gitx"
	"github.com/lionelchamorro/orquestalite/internal/results"
)

// stubReviewCaller returns a fixed critic verdict for the review orchestration.
type stubReviewCaller struct {
	out *results.CriticResult
	got string // the diff it received
}

func (s *stubReviewCaller) RunReview(_ context.Context, diff string) (*results.CriticResult, error) {
	s.got = diff
	return s.out, nil
}

// fakeGHPR records the review event/body posted via gh, and resolves a PR.
type fakeGHPR struct {
	pr      string
	base    string
	head    string
	resolve string // if non-empty, ResolvePR returns this error
	posted  []postedReview
	postErr error
}

type postedReview struct {
	PR    string
	Event string
	Body  string
}

func (f *fakeGHPR) ResolvePR(_ context.Context, pr string) (string, string, error) {
	if f.resolve != "" {
		return "", "", errors.New(f.resolve)
	}
	f.pr = pr
	return f.base, f.head, nil
}

func (f *fakeGHPR) PostReview(_ context.Context, pr, event, body string) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posted = append(f.posted, postedReview{PR: pr, Event: event, Body: body})
	return nil
}

// makeFileCommit commits a single file in dir.
func makeFileCommit(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("git", "add", "-A")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	c = exec.Command("git", "commit", "-q", "-m", msg)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func TestDiffRefs_AssemblesDiffBetweenTwoRefs(t *testing.T) {
	dir := initTestRepo(t)
	makeFileCommit(t, dir, "a.txt", "A\n", "add a")
	base, _ := gitx.HeadSHA(dir)
	makeFileCommit(t, dir, "b.txt", "B\n", "add b")
	head, _ := gitx.HeadSHA(dir)

	diff, err := gitx.DiffRefs(dir, base, head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+B") || !strings.Contains(diff, "b.txt") {
		t.Fatalf("diff missing b.txt addition:\n%s", diff)
	}
	if strings.Contains(diff, "a.txt") {
		t.Fatalf("diff should not include unchanged a.txt:\n%s", diff)
	}
}

func TestReview_ResolvedPRToPostedVerdict(t *testing.T) {
	dir := initTestRepo(t)
	makeFileCommit(t, dir, "a.txt", "A\n", "add a")
	base, _ := gitx.HeadSHA(dir)
	makeFileCommit(t, dir, "b.txt", "B\n", "add b")
	head, _ := gitx.HeadSHA(dir)

	gh := &fakeGHPR{base: base, head: head}
	stub := &stubReviewCaller{out: &results.CriticResult{
		Status: "rejected",
		Concerns: []results.Concern{
			{Severity: "blocker", Where: "b.txt:1", Issue: "missing newline handling", Suggestion: "add EOF check"},
			{Severity: "nit", Where: "b.txt:2", Issue: "style"},
		},
	}}
	var out bytes.Buffer
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: dir,
		PR:         "42",
		Publish:    true,
		Out:        &out,
	}, gh, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		return stub, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The diff was assembled from the gh-resolved refs and handed to the critic.
	if !strings.Contains(stub.got, "b.txt") {
		t.Errorf("critic did not receive the PR diff, got:\n%s", stub.got)
	}
	// A rejected review posts REQUEST_CHANGES with the concerns rendered.
	if len(gh.posted) != 1 {
		t.Fatalf("posted reviews = %d, want 1", len(gh.posted))
	}
	p := gh.posted[0]
	if p.PR != "42" || p.Event != "REQUEST_CHANGES" {
		t.Errorf("posted PR=%s event=%s, want 42/REQUEST_CHANGES", p.PR, p.Event)
	}
	for _, want := range []string{"Changes requested", "missing newline handling", "add EOF check", "b.txt:1"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body missing %q:\n%s", want, p.Body)
		}
	}
	if !strings.Contains(out.String(), "CHANGES REQUESTED") {
		t.Errorf("stdout missing verdict: %q", out.String())
	}
}

func TestReview_ApprovedPostsApprove(t *testing.T) {
	dir := initTestRepo(t)
	makeFileCommit(t, dir, "a.txt", "A\n", "add a")
	base, _ := gitx.HeadSHA(dir)
	makeFileCommit(t, dir, "b.txt", "B\n", "add b")
	head, _ := gitx.HeadSHA(dir)

	gh := &fakeGHPR{base: base, head: head}
	stub := &stubReviewCaller{out: &results.CriticResult{Status: "approved"}}
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: dir, PR: "7", Publish: true, Out: &bytes.Buffer{},
	}, gh, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		return stub, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.posted) != 1 || gh.posted[0].Event != "APPROVE" {
		t.Fatalf("expected APPROVE, got %+v", gh.posted)
	}
	if !strings.Contains(gh.posted[0].Body, "Approved by orq-lite review") {
		t.Errorf("approve body = %q", gh.posted[0].Body)
	}
}

func TestReview_ExplicitRefsSkipResolve(t *testing.T) {
	dir := initTestRepo(t)
	makeFileCommit(t, dir, "a.txt", "A\n", "add a")
	base, _ := gitx.HeadSHA(dir)
	makeFileCommit(t, dir, "b.txt", "B\n", "add b")
	head, _ := gitx.HeadSHA(dir)

	gh := &fakeGHPR{base: "should-not-be-used", head: "should-not-be-used"}
	called := false
	ghResolve_capture := &struct{ called bool }{}
	_ = ghResolve_capture
	called = false
	stub := &stubReviewCaller{out: &results.CriticResult{Status: "approved"}}
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: dir, Base: base, Head: head, Out: &bytes.Buffer{},
	}, gh, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		called = true
		return stub, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ResolvePR must not be consulted when both refs are explicit.
	if gh.pr != "" {
		t.Errorf("ResolvePR was called (pr=%q) but refs were explicit", gh.pr)
	}
	if !called {
		t.Error("caller factory not invoked")
	}
	if !strings.Contains(stub.got, "b.txt") {
		t.Errorf("diff not assembled: %q", stub.got)
	}
}

func TestReview_NoRefsNorPRErrors(t *testing.T) {
	gh := &fakeGHPR{}
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: t.TempDir(), Out: &bytes.Buffer{},
	}, gh, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		t.Fatal("caller should not be built without refs")
		return nil, func() error { return nil }, nil
	})
	if err == nil || !strings.Contains(err.Error(), "requires a PR number") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestReview_ResolveFailurePropagates(t *testing.T) {
	gh := &fakeGHPR{resolve: "no such pr"}
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: t.TempDir(), PR: "99", Out: &bytes.Buffer{},
	}, gh, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		return &stubReviewCaller{}, func() error { return nil }, nil
	})
	if err == nil || !strings.Contains(err.Error(), "no such pr") {
		t.Fatalf("expected resolve error, got %v", err)
	}
}

func TestReview_EmptyDiffNoOps(t *testing.T) {
	dir := initTestRepo(t)
	makeFileCommit(t, dir, "a.txt", "A\n", "add a")
	sha, _ := gitx.HeadSHA(dir)
	var out bytes.Buffer
	err := ReviewWith(context.Background(), ReviewOptions{
		ProjectDir: dir, Base: sha, Head: sha, Out: &out,
	}, &fakeGHPR{}, func(ReviewOptions) (ReviewCriticCaller, func() error, error) {
		t.Fatal("caller should not run on empty diff")
		return nil, func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to review") {
		t.Errorf("stdout = %q", out.String())
	}
}
