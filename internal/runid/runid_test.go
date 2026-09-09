package runid

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"testing"
	"time"
)

// TestNew_Format verifies the returned ID matches the documented compact-UTC
// + 16 random hex bytes shape: r20060102T150405Z-a1b2.
func TestNew_Format(t *testing.T) {
	re := regexp.MustCompile(`^r\d{8}T\d{6}Z-[0-9a-f]{32}$`)
	id := New()
	if !re.MatchString(id) {
		t.Fatalf("id %q does not match expected format", id)
	}
}

// TestNew_Unique verifies two calls return distinct IDs (the random suffix
// makes a collision astronomically unlikely).
func TestNew_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("collision after %d IDs: %q", i, id)
		}
		seen[id] = true
	}
}

func TestGenerateDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	id, err := generate(now, bytes.NewReader(make([]byte, 16)))
	if err != nil || id != "r20260908T120000Z-00000000000000000000000000000000" {
		t.Fatalf("%s %v", id, err)
	}
	for _, n := range []int{0, 15} {
		id, err = generate(now, bytes.NewReader(make([]byte, n)))
		if id != "" || !(errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			t.Fatalf("short entropy: %q %v", id, err)
		}
	}
}
