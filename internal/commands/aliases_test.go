package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunDevelopmentAliasRejectsUnknownCommand(t *testing.T) {
	err := RunDevelopmentAlias(context.Background(), t.TempDir(), "unknown", nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `no development flow alias for "unknown"`) {
		t.Fatalf("error = %v", err)
	}
}
