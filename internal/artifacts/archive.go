package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
)

// ArchiveResult writes an immutable copy of one agent result. Repeated
// execution of the same durable attempt receives a rerun suffix rather than
// overwriting prior evidence.
func ArchiveResult(projectDir, role, taskID string, cycle, attempt int, raw []byte) error {
	if taskID == "" {
		taskID = "_workflow"
	}
	base := filepath.Join(projectDir, ".orquestalite", "results", "by-task", taskID, fmt.Sprintf("%s.c%d.a%d.json", role, cycle, attempt))
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return fmt.Errorf("create result archive: %w", err)
	}
	path := base
	for rerun := 1; ; rerun++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, writeErr := file.Write(raw); writeErr != nil {
				_ = file.Close()
				return fmt.Errorf("write result archive %s: %w", path, writeErr)
			}
			if closeErr := file.Close(); closeErr != nil {
				return fmt.Errorf("close result archive %s: %w", path, closeErr)
			}
			return nil
		}
		if !os.IsExist(err) {
			return fmt.Errorf("create result archive %s: %w", path, err)
		}
		path = base[:len(base)-len(".json")] + fmt.Sprintf(".r%d.json", rerun+1)
	}
}
