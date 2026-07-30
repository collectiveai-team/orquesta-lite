package flow

import (
	"reflect"
	"testing"
)

func TestCompareVersionsOrdersSemverNotLexically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1", "2", -1},
		{"2", "1", 1},
		// A missing field counts as zero, so these three are one version.
		{"1", "1.0", 0},
		{"1.0", "1.0.0", 0},
		{"1", "1.0.0", 0},
		// Numeric, not lexical: "1.10" > "1.2" even though "1.10" < "1.2" as text.
		{"1.2", "1.10", -1},
		{"1.10", "1.2", 1},
		{"1.2.3", "1.2.10", -1},
		// A prerelease sorts below the release it precedes.
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-1", "1.0.0-2", -1},
		{"1.0.0-11", "1.0.0-2", 1},
		{"1.0.0-alpha", "1.0.0-alpha", 0},
		// Build metadata is ignored for precedence.
		{"1.0.0+build1", "1.0.0+build2", 0},
		{"1.0.0+build", "1.0.1", -1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortVersionsPutsHighestLast(t *testing.T) {
	got := SortVersions([]string{"2", "10", "1.9", "1.10", "3.0.0-rc.1", "3"})
	want := []string{"1.9", "1.10", "2", "3.0.0-rc.1", "3", "10"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestIsValidVersionMatchesVersionPattern(t *testing.T) {
	for _, valid := range []string{"1", "1.2", "1.2.3", "1.2.3-rc.1", "1.2.3+build"} {
		if !IsValidVersion(valid) {
			t.Errorf("%q should be a valid version", valid)
		}
	}
	for _, invalid := range []string{"", "v1", "1.2.3.4", "latest", ".install-tmp-123"} {
		if IsValidVersion(invalid) {
			t.Errorf("%q should not be a valid version", invalid)
		}
	}
}
