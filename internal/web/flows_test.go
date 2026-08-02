package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lionelchamorro/orquestalite/internal/commands"
)

func TestAPIFlowsIgnoresLegacyCatalog(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), []byte(`{"flows":{"legacy-only":{"steps":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{Dir: dir}
	var response struct {
		Flows []struct {
			Name string `json:"name"`
		} `json:"flows"`
	}
	if code := getJSON(t, server, "/api/flows", &response); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if len(response.Flows) != 0 {
		t.Fatalf("legacy catalog leaked into response: %+v", response.Flows)
	}
}

func TestAPIFlowsListsVerifiedInstalledPack(t *testing.T) {
	dir := t.TempDir()
	if err := commands.InitWithOptions(dir, commands.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Dir: dir}
	var response struct {
		Flows []struct {
			Name       string               `json:"name"`
			Pack       string               `json:"pack"`
			PackDigest string               `json:"pack_digest"`
			Inputs     map[string]flowInput `json:"inputs"`
			Roles      []string             `json:"roles"`
			Preflight  map[string]string    `json:"preflight"`
		} `json:"flows"`
	}
	if code := getJSON(t, server, "/api/flows", &response); code != 200 {
		t.Fatalf("code=%d", code)
	}
	found := false
	for _, entry := range response.Flows {
		if entry.Name != "development/task-list@1" {
			continue
		}
		found = true
		if entry.Pack != "development@4" || entry.PackDigest == "" {
			t.Fatalf("pack metadata = %+v", entry)
		}
		if _, ok := entry.Inputs["fast"]; !ok || len(entry.Roles) == 0 || !strings.Contains(strings.Join(entry.Roles, ","), "ticket_planner") {
			t.Fatalf("catalog entry = %+v", entry)
		}
	}
	if !found {
		t.Fatalf("task-list flow missing: %+v", response.Flows)
	}
}
