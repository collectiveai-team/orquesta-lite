package builtin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "orq-lite tests"},
		{"config", "user.email", "tests@orq-lite.invalid"},
	} {
		command := exec.Command("git", argv...)
		command.Dir = dir
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", argv, err, out)
		}
	}
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// installHook writes an executable pre-commit hook with the given shell body.
func installHook(t *testing.T, dir, body string) {
	t.Helper()
	writeFile(t, dir, filepath.Join(".git", "hooks", "pre-commit"), "#!/bin/sh\n"+body+"\n")
}

func runCommit(t *testing.T, dir string, input map[string]any) (gitCommitOutput, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	executor := &GitCommitExecutor{DefaultDir: dir}
	result, execErr := executor.Execute(context.Background(), activity.Request{Inputs: raw, StepID: "commit"})
	var output gitCommitOutput
	if len(result.Output) > 0 {
		if err = json.Unmarshal(result.Output, &output); err != nil {
			t.Fatalf("output is not decodable: %v (%s)", err, result.Output)
		}
	}
	return output, execErr
}

func gitLog(t *testing.T, dir string) string {
	t.Helper()
	command := exec.Command("git", "log", "--pretty=%s%n%b")
	command.Dir = dir
	out, err := command.CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func TestCommitStagesAndCommitsEverything(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "app.go", "package app\n")
	output, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": "add app"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !output.Committed {
		t.Fatalf("want committed, got %+v", output)
	}
	if !strings.Contains(gitLog(t, dir), "feat(T-1): add app") {
		t.Fatalf("commit subject not in the log:\n%s", gitLog(t, dir))
	}
}

func TestCommitMessageFollowsConventionalCommits(t *testing.T) {
	// The project's prek config rejects any subject that does not.
	dir := gitRepo(t)
	writeFile(t, dir, "a.txt", "x")
	if _, err := runCommit(t, dir, map[string]any{"type": "fix", "scope": "ENG-9", "subject": "stop the leak", "body": "details here"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	log := gitLog(t, dir)
	if !strings.Contains(log, "fix(ENG-9): stop the leak") || !strings.Contains(log, "details here") {
		t.Fatalf("message not assembled as expected:\n%s", log)
	}
}

func TestCommitTruncatesALongSubjectToOneShortLine(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "a.txt", "x")
	long := strings.Repeat("very long ticket title ", 10) + "\nsecond line"
	if _, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": long}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	subject := strings.SplitN(gitLog(t, dir), "\n", 2)[0]
	if len(subject) > 72 {
		t.Fatalf("subject is %d chars, want <= 72: %q", len(subject), subject)
	}
	if strings.Contains(subject, "second line") {
		t.Fatalf("a multi-line subject must be cut at the first line: %q", subject)
	}
}

func TestCommitIsASkipWhenNotEnabled(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "a.txt", "x")
	output, err := runCommit(t, dir, map[string]any{"enabled": false, "type": "feat", "scope": "T-1", "subject": "t"})
	if err != nil {
		t.Fatalf("a disabled commit must succeed: %v", err)
	}
	if output.Committed || output.Blocked {
		t.Fatalf("want a clean skip, got %+v", output)
	}
	if gitLog(t, dir) != "" {
		t.Fatalf("a disabled commit must not commit:\n%s", gitLog(t, dir))
	}
}

func TestCommitIsGreenWhenThereIsNothingToCommit(t *testing.T) {
	dir := gitRepo(t)
	output, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": "t"})
	if err != nil {
		t.Fatalf("an empty tree must not fail the run: %v", err)
	}
	if output.Committed || output.Blocked {
		t.Fatalf("want a clean no-op, got %+v", output)
	}
}

func TestCommitRetriesAfterAHookAutoFixesFiles(t *testing.T) {
	// end-of-file-fixer and trailing-whitespace behave exactly like this: they
	// rewrite the staged files and fail, and the same commit then succeeds.
	dir := gitRepo(t)
	installHook(t, dir, `
if [ ! -f .fixed ]; then
  touch .fixed
  printf 'fixed\n' >> app.go
  exit 1
fi
exit 0`)
	writeFile(t, dir, "app.go", "package app\n")
	output, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": "add app"})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !output.Committed || !output.Retried {
		t.Fatalf("want a committed retry, got %+v", output)
	}
	if output.Blocked {
		t.Fatalf("a recovered commit must not report blocked: %+v", output)
	}
}

func TestCommitReportsBlockedWithoutFailingTheRun(t *testing.T) {
	dir := gitRepo(t)
	installHook(t, dir, `echo "E501 line too long" >&2; exit 1`)
	writeFile(t, dir, "app.go", "package app\n")
	output, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": "add app"})
	if err != nil {
		t.Fatalf("a blocked commit must not abort the run: %v", err)
	}
	if output.Committed || !output.Blocked {
		t.Fatalf("want blocked, got %+v", output)
	}
	if !strings.Contains(output.Stderr+output.Stdout, "E501 line too long") {
		t.Fatalf("the hook output must reach the flow so an agent can fix it: %+v", output)
	}
}

func TestRequireCommittedTurnsABlockedCommitIntoAGateFailure(t *testing.T) {
	dir := gitRepo(t)
	installHook(t, dir, `echo "still broken" >&2; exit 1`)
	writeFile(t, dir, "app.go", "package app\n")
	_, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1", "subject": "add app", "requireCommitted": true})
	if err == nil {
		t.Fatal("requireCommitted must fail the run when the commit cannot land")
	}
	var activityErr *activity.Error
	if !asActivityError(err, &activityErr) || activityErr.Class != activity.ErrorGateFailed {
		t.Fatalf("want a gate failure, got %T %v", err, err)
	}
}

func TestCommitRejectsADirectoryThatIsNotAGitRepository(t *testing.T) {
	output, err := runCommit(t, t.TempDir(), map[string]any{"type": "feat", "scope": "T-1", "subject": "t"})
	if err == nil {
		t.Fatalf("want an error outside a repository, got %+v", output)
	}
}

func TestCommitRequiresASubject(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "a.txt", "x")
	if _, err := runCommit(t, dir, map[string]any{"type": "feat", "scope": "T-1"}); err == nil {
		t.Fatal("a commit with no subject is a contract error, not a default message")
	}
}

// A ticket title in Spanish is enough to produce an invalid commit message:
// cutting the subject at a byte offset can land inside a multibyte character
// and leave the message ending in a lone continuation byte.
func TestCommitSubjectStaysValidUTF8WhenTheLimitSplitsACharacter(t *testing.T) {
	subject := "Listar las conversaciones con el estado del pipeline y el botón Ver más"
	message := commitMessage(gitCommitInput{Type: "feat", Scope: "T2", Subject: subject})
	if !utf8.ValidString(message) {
		t.Fatalf("subject was cut mid-character: %q (% x)", message, message)
	}
	if len(message) > subjectLimit {
		t.Fatalf("message is %d bytes, want <= %d: %q", len(message), subjectLimit, message)
	}
}

// The reported failure: commit-msg hooks read the message with a strict UTF-8
// decoder, so an invalid byte sequence crashes them before they can lint it.
// The commit is refused, and no agent can repair a message the flow built.
func TestCommitSurvivesAHookThatDecodesTheMessageAsUTF8(t *testing.T) {
	if _, err := exec.LookPath("iconv"); err != nil {
		t.Skip("iconv is needed to validate the message encoding")
	}
	dir := gitRepo(t)
	writeFile(t, dir, filepath.Join(".git", "hooks", "commit-msg"),
		"#!/bin/sh\niconv -f UTF-8 -t UTF-8 \"$1\" >/dev/null\n")
	writeFile(t, dir, "app.go", "package app\n")
	output, err := runCommit(t, dir, map[string]any{
		"type": "feat", "scope": "T2",
		"subject":          "Listar las conversaciones con el estado del pipeline y el botón Ver más",
		"requireCommitted": true,
	})
	if err != nil {
		t.Fatalf("a valid UTF-8 ticket title must not fail the run: %v", err)
	}
	if !output.Committed {
		t.Fatalf("want committed, got %+v", output)
	}
}

// "…y el bot" is a worse subject than "…y el": the first line is prose that a
// human and a commit linter both read, so the cut belongs between words.
func TestCommitSubjectIsCutAtAWordBoundary(t *testing.T) {
	subject := "Listar las conversaciones con el estado del pipeline y el botón Ver más"
	message := commitMessage(gitCommitInput{Type: "feat", Scope: "T2", Subject: subject})
	kept := strings.TrimPrefix(message, "feat(T2): ")
	rest := strings.TrimPrefix(subject, kept)
	if rest != "" && !strings.HasPrefix(rest, " ") {
		t.Fatalf("subject cut mid-word: kept %q, next %q", kept, rest)
	}
}

// A subject with no space to back off to must keep as much as fits rather than
// collapse to the first short word.
func TestCommitKeepsALongUnbrokenSubjectInsteadOfCollapsingIt(t *testing.T) {
	subject := "Implement " + strings.Repeat("Camel", 20)
	message := commitMessage(gitCommitInput{Type: "feat", Scope: "T2", Subject: subject})
	if len(message) < subjectLimit/2 {
		t.Fatalf("truncation threw away a usable subject: %q", message)
	}
	if !utf8.ValidString(message) || len(message) > subjectLimit {
		t.Fatalf("message is %d bytes and valid=%v: %q", len(message), utf8.ValidString(message), message)
	}
}
