package commands

import (
	"errors"
	"os"
	"path/filepath"
)

func Reset(projectDir string) error {
	p := filepath.Join(projectDir, ".orquestalite")
	err := os.RemoveAll(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
