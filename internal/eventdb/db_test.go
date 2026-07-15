package eventdb

import (
	"path/filepath"
	"testing"
)

func TestOpen_CreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path) // reopen: schema already present, must not error
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{"runs", "agent_runs", "events", "ingest_state"} {
		var n int
		if err := db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var mode string
	if err := db.sql.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestOpen_RejectsUnknownSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orq.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("want error for unknown schema version")
	}
}
