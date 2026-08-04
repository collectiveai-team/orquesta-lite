package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFactoryHelpAdvertisesFastAsTheDefault(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "factory", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("orq-lite factory --help: %v\n%s", err, out)
	}
	help := string(out)
	start := strings.Index(help, "  -fast\n")
	if start < 0 {
		t.Fatalf("factory help has no -fast flag:\n%s", help)
	}
	fastHelp := help[start:]
	if end := strings.Index(fastHelp[1:], "\n  -"); end >= 0 {
		fastHelp = fastHelp[:end+1]
	}
	if !strings.Contains(fastHelp, "(default true)") {
		t.Fatalf("factory help must advertise fast=true as the default:\n%s", help)
	}
}
