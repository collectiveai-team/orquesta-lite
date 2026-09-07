package web

import (
	"os"
	"testing"
)

// TestMain gives the test process an explicit git identity. Several tests here
// run `orq-lite init`, which creates a repository and makes an initial commit,
// and git refuses to commit without an author. Without these variables git
// guesses one from the OS account, which works on a developer workstation and
// fails on a machine whose account has no full name — a CI runner, a container,
// a fresh image — with "fatal: empty ident name". Setting them here keeps the
// suite hermetic rather than configuring the machine around it.
func TestMain(m *testing.M) {
	for name, value := range map[string]string{
		"GIT_AUTHOR_NAME":     "orq-lite tests",
		"GIT_AUTHOR_EMAIL":    "tests@orq-lite.invalid",
		"GIT_COMMITTER_NAME":  "orq-lite tests",
		"GIT_COMMITTER_EMAIL": "tests@orq-lite.invalid",
	} {
		if err := os.Setenv(name, value); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}
