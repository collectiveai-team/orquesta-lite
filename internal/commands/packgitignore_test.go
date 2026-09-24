package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The repo-wide .gitignore ignores `prompts/` and `schemas/` so a dogfooding
// workspace does not commit its own runtime config. The shipped pack has
// directories by those names, and its files stay tracked across a move because
// git does not untrack what it already tracks — so the day the pack moved out
// of examples/, every existing file kept working and only the NEXT file added
// to it would have vanished silently, with pack.json listing a digest for a
// file no fresh clone contains.
//
// This probes a name that does not exist rather than the files that do:
// check-ignore reports nothing for a tracked path, so asking about the real
// files would answer the wrong question and pass for the wrong reason.
func TestEveryPackDirectoryAcceptsNewFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está en PATH")
	}
	repoRoot := filepath.Join("..", "..")
	packsRoot := filepath.Join(repoRoot, "packs")

	var probes []string
	err := filepath.WalkDir(packsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		probes = append(probes, filepath.ToSlash(filepath.Join(relative, "__probe__.json")))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatal("no se encontró ningún directorio bajo packs/")
	}

	command := exec.Command("git", append([]string{"check-ignore", "--no-index", "--stdin"}, nil...)...)
	command.Dir = repoRoot
	command.Stdin = strings.NewReader(strings.Join(probes, "\n") + "\n")
	out, _ := command.Output()

	if ignored := strings.TrimSpace(string(out)); ignored != "" {
		t.Fatalf("estos directorios del pack rechazarían un archivo nuevo por .gitignore, "+
			"y pack.json terminaría listando un digest que ningún clone tiene:\n%s\n\n"+
			"agregá la negación correspondiente (`!packs/**/<dir>/` y `!packs/**/<dir>/**`) en .gitignore", ignored)
	}
}
