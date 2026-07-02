package web

import (
	"testing"
)

func TestAPIDoctor_ReportsChecksAndCaches(t *testing.T) {
	srv := &Server{Dir: stateDir(t)} // empty project: team.json check errors

	var resp struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if code := getJSON(t, srv, "/api/doctor", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if resp.OK {
		t.Fatal("ok = true for empty project, want false (team.json missing)")
	}
	found := false
	for _, c := range resp.Checks {
		if c.Name == "team.json" && c.Status == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no team.json error check in %+v", resp.Checks)
	}

	// Second request within 30s serves the cached bytes.
	first := srv.doctorCached
	if code := getJSON(t, srv, "/api/doctor", &resp); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if string(srv.doctorCached) != string(first) || srv.doctorCached == nil {
		t.Fatal("cache not reused within TTL")
	}
}
