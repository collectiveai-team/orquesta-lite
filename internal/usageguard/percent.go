package usageguard

import (
	"fmt"
	"math"
)

// Percent is the canonical consumption reading shared by every provider: the
// share of a usage window already consumed, on a 0-100 scale.
//
// Providers report consumption in their own terms - Claude as `utilization`,
// Codex as `used_percent`, Claude's interactive panel sometimes as headroom
// rather than consumption. Every reader converts at its parse boundary through
// the constructors below, so a raw provider number can never reach a threshold
// comparison without having declared which convention it came from.
//
// The scale itself cannot be inferred from a single value: 0.04 is a valid
// reading both as "0.04% consumed" and as a fraction meaning "4% consumed".
// That is why the convention is declared per provider rather than guessed, and
// why a provider without a converter is rejected instead of assumed.
type Percent float64

// Used converts a value already expressed as percent-consumed on the 0-100
// scale. This is the convention Claude's `utilization` and Codex's
// `used_percent` both use.
func Used(raw float64) (Percent, error) {
	return validPercent(raw, "used")
}

// Remaining converts a value expressed as percent-still-available on the 0-100
// scale, inverting it into the canonical consumed form. Claude's interactive
// usage panel reports headroom this way ("12% left").
func Remaining(raw float64) (Percent, error) {
	percent, err := validPercent(raw, "remaining")
	if err != nil {
		return 0, err
	}
	return 100 - percent, nil
}

func validPercent(raw float64, kind string) (Percent, error) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 0, fmt.Errorf("%s percentage is not a finite number", kind)
	}
	if raw < 0 || raw > 100 {
		return 0, fmt.Errorf("%s percentage %g is outside the 0-100 scale", kind, raw)
	}
	return Percent(raw), nil
}
