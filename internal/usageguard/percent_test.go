package usageguard

import (
	"math"
	"testing"
)

func TestUsedAcceptsTheCanonicalScale(t *testing.T) {
	for _, tc := range []struct {
		raw  float64
		want Percent
	}{
		{0, 0}, {0.04, 0.04}, {41, 41}, {99.7, 99.7}, {100, 100},
	} {
		got, err := Used(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("Used(%g) = %v, %v; want %v, nil", tc.raw, got, err, tc.want)
		}
	}
}

func TestRemainingInvertsIntoConsumed(t *testing.T) {
	for _, tc := range []struct {
		raw  float64
		want Percent
	}{
		{0, 100}, {12, 88}, {100, 0},
	} {
		got, err := Remaining(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("Remaining(%g) = %v, %v; want %v, nil", tc.raw, got, err, tc.want)
		}
	}
}

// A reading outside the declared scale is a broken provider contract, not a
// value to be clamped: clamping would silently keep the guard running against
// a number it cannot interpret.
func TestPercentRejectsValuesOffTheDeclaredScale(t *testing.T) {
	for _, raw := range []float64{-0.1, 100.1, 150, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := Used(raw); err == nil {
			t.Fatalf("Used(%g) should be rejected", raw)
		}
		if _, err := Remaining(raw); err == nil {
			t.Fatalf("Remaining(%g) should be rejected", raw)
		}
	}
}
