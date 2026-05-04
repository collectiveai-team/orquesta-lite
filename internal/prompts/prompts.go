package prompts

import (
	"fmt"
	"os"
	"strings"
)

func Load(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("load prompt %s: %w", path, err)
	}
	return string(raw), nil
}

func Interpolate(tmpl string, vars map[string]string) string {
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}
