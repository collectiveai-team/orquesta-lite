package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPIFlows_EmptyWhenMissingOrInvalid(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // no flows.json
	var resp struct {
		Flows []any `json:"flows"`
	}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.Flows == nil || len(resp.Flows) != 0 {
		t.Fatalf("resp = %+v, want empty non-null flows", resp)
	}

	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv = &Server{Dir: dir}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 || len(resp.Flows) != 0 {
		t.Fatalf("invalid flows.json: code=%d resp=%+v", code, resp)
	}
}

func TestAPIFlows_CatalogWithPreflight(t *testing.T) {
	dir := stateDir(t)
	// coder exists with a prompt on disk; critic exists but its prompt file
	// is missing; parser is not declared at all.
	flowsJSON := `{"flows":{"build":{
		"description":"build a thing",
		"inputs":{
			"features_file":{"type":"string"},
			"max_cycles":{"type":"number","default":3}
		},
		"steps":[
			{"type":"agent","agent":"parser"},
			{"type":"loop","iterator":"{xs}","as":"x","body":[
				{"type":"retry_until","condition":"{ok} == true","body":[
					{"type":"agent","agent":"coder"},
					{"type":"agent","agent":"critic"}
				]}
			]}
		]}}}`
	teamJSON := `{
		"agents":{"sonnet":{"provider":"claude","model":"claude-sonnet-4-6"}},
		"roles":{
			"coder":{"agents":["sonnet"],"prompt":"prompts/coder.md","result_path":".orquestalite/results/coder.json","timeout_seconds":60},
			"critic":{"agents":["sonnet"],"prompt":"prompts/critic.md","result_path":".orquestalite/results/critic.json","timeout_seconds":60}
		},
		"limits":{"max_review_cycles":1,"max_fix_iterations":1},
		"rate_limit_backoff":{"initial_seconds":1,"factor":2,"max_seconds":2}
	}`
	if err := os.WriteFile(filepath.Join(dir, "flows.json"), []byte(flowsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.json"), []byte(teamJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompts", "coder.md"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &Server{Dir: dir}
	var resp struct {
		Flows []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Inputs      map[string]struct {
				Type     string `json:"type"`
				Default  any    `json:"default"`
				Required bool   `json:"required"`
			} `json:"inputs"`
			Roles     []string          `json:"roles"`
			Preflight map[string]string `json:"preflight"`
		} `json:"flows"`
	}
	if code := getJSON(t, srv, "/api/flows", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if len(resp.Flows) != 1 || resp.Flows[0].Name != "build" {
		t.Fatalf("resp = %+v", resp)
	}
	f := resp.Flows[0]
	if !f.Inputs["features_file"].Required || f.Inputs["features_file"].Default != nil {
		t.Fatalf("features_file = %+v", f.Inputs["features_file"])
	}
	if f.Inputs["max_cycles"].Required || f.Inputs["max_cycles"].Default != 3.0 {
		t.Fatalf("max_cycles = %+v", f.Inputs["max_cycles"])
	}
	want := []string{"coder", "critic", "parser"}
	if len(f.Roles) != 3 || f.Roles[0] != want[0] || f.Roles[1] != want[1] || f.Roles[2] != want[2] {
		t.Fatalf("roles = %v", f.Roles)
	}
	if f.Preflight["coder"] != "ok" || f.Preflight["critic"] != "missing_prompt" || f.Preflight["parser"] != "missing_role" {
		t.Fatalf("preflight = %v", f.Preflight)
	}
}
