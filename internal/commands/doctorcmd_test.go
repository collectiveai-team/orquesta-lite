package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/collectiveai-team/orquesta-lite/internal/doctor"
)

func TestDoctorMissingTeamJSONFails(t *testing.T) {
	var out strings.Builder
	if err := Doctor(t.TempDir(), &out); err == nil {
		t.Fatal("expected failure without team.json")
	}
	if !strings.Contains(out.String(), "[FAIL] team.json") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestDoctorRecognizesInitializedDevelopmentPack(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	checks := doctor.Run(context.Background(), dir)
	for _, check := range checks {
		if check.Name == "pack:development" {
			if check.Status != doctor.StatusOK {
				t.Fatalf("pack check = %+v", check)
			}
			return
		}
	}
	t.Fatalf("pack check missing: %+v", checks)
}
