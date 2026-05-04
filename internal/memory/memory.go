package memory

import (
	"errors"
	"fmt"
	"os"
)

type Entry struct {
	Cycle  int
	TaskID string
	Role   string
	Body   string
}

func Append(path string, e Entry) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open memory: %w", err)
	}
	defer f.Close()
	header := fmt.Sprintf("\n## [cycle %d, task %s, %s]\n", e.Cycle, e.TaskID, e.Role)
	if _, err := f.WriteString(header + e.Body + "\n"); err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	return nil
}

func ReadAll(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
