package commands

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lionelchamorro/orquestalite/internal/eventdb"
)

// Index builds or tops up the sqlite read-model (.orquestalite/orq.db) from
// run.log and rotated archives — the headless counterpart of the ingestion
// `orq-lite serve` performs continuously. rebuild deletes the db first and
// re-ingests the full history.
func Index(projectDir string, rebuild bool, out io.Writer) error {
	stateDir := filepath.Join(projectDir, ".orquestalite")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(stateDir, "orq.db")
	if rebuild {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	db, err := eventdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ingest(stateDir); err != nil {
		return err
	}
	runs, agentRuns, events, err := db.Counts()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "indexed: %d runs, %d agent runs, %d events -> %s\n", runs, agentRuns, events, dbPath)
	return nil
}
