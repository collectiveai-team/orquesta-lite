package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPackListReportsInstalledPacksInVersionOrder(t *testing.T) {
	dir := t.TempDir()
	installFixturePack(t, dir, "development", "2", nil)
	installFixturePack(t, dir, "development", "10", nil)
	installFixturePack(t, dir, "development", "1", nil)
	installFixturePack(t, dir, "neutral", "1", nil)
	var out bytes.Buffer
	if err := PackCLI(context.Background(), dir, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected one line per installed pack, got %d:\n%s", len(lines), out.String())
	}
	// Ascending version order, numeric not lexical: 10 comes after 2.
	for index, prefix := range []string{"development@1 ", "development@2 ", "development@10 ", "neutral@1 "} {
		if !strings.HasPrefix(lines[index], prefix) {
			t.Errorf("line %d = %q, want prefix %q", index, lines[index], prefix)
		}
	}
	// Only the version an unpinned ref resolves to is marked default.
	if !strings.HasSuffix(lines[2], "(default)") {
		t.Errorf("the highest development version must be marked default: %q", lines[2])
	}
	if strings.Contains(lines[0], "(default)") || strings.Contains(lines[1], "(default)") {
		t.Errorf("only one default per pack name:\n%s", out.String())
	}
	if !strings.HasSuffix(lines[3], "(default)") {
		t.Errorf("a single installed version is its own default: %q", lines[3])
	}
	// Digest and file count are reported so a caller can verify what it has.
	if !strings.Contains(lines[0], "digest=") || !strings.Contains(lines[0], "files=2") {
		t.Errorf("line = %q, want digest and file count (1 flow + pack.json)", lines[0])
	}
}

func TestPackListWithNothingInstalled(t *testing.T) {
	var out bytes.Buffer
	if err := PackCLI(context.Background(), t.TempDir(), []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no packs installed") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestPackCLIRejectsUnknownSubcommands(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{}, {"uninstall"}, {"list", "extra"}} {
		if err := PackCLI(context.Background(), t.TempDir(), args, &out); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
}
