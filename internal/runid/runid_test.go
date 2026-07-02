package runid

import (
	"regexp"
	"testing"
)

// TestNew_Format verifies the returned ID matches the documented compact-UTC
// + 2 random hex bytes shape: r20060102T150405Z-a1b2.
func TestNew_Format(t *testing.T) {
	re := regexp.MustCompile(`^r\d{8}T\d{6}Z-[0-9a-f]{4}$`)
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