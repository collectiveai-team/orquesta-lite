package fallback

import (
	"testing"
	"time"
)

func TestParseResetTime(t *testing.T) {
	// A fixed "now": 2026-06-16 13:50:00 local.
	now := time.Date(2026, 6, 16, 13, 50, 0, 0, time.Local)

	cases := []struct {
		name    string
		text    string
		wantOK  bool
		wantUTC time.Time // expected reset (local), zero when wantOK is false
	}{
		{
			name:    "codex try again at clock pm",
			text:    "You've hit your usage limit. ... or try again at 4:30 PM.",
			wantOK:  true,
			wantUTC: time.Date(2026, 6, 16, 16, 30, 0, 0, time.Local),
		},
		{
			name:    "24h clock",
			text:    "rate limited, available at 16:05",
			wantOK:  true,
			wantUTC: time.Date(2026, 6, 16, 16, 5, 0, 0, time.Local),
		},
		{
			name:    "claude session limit resets Npm (no 'at')",
			text:    "You've hit your session limit · resets 7pm (America/Buenos_Aires)",
			wantOK:  true,
			wantUTC: time.Date(2026, 6, 16, 19, 0, 0, 0, time.Local),
		},
		{
			name:    "resets at with space and am/pm (future)",
			text:    "quota exceeded; resets at 6 pm",
			wantOK:  true,
			wantUTC: time.Date(2026, 6, 16, 18, 0, 0, 0, time.Local),
		},
		{
			name:   "clock already past yields no reset (caller uses backoff, not +24h)",
			text:   "try again at 9:00 AM",
			wantOK: false,
		},
		{
			name:    "relative seconds",
			text:    "429 Too Many Requests. try again in 30 seconds",
			wantOK:  true,
			wantUTC: now.Add(30 * time.Second),
		},
		{
			name:    "relative minutes",
			text:    "retry after 5 minutes",
			wantOK:  true,
			wantUTC: now.Add(5 * time.Minute),
		},
		{
			name:    "retry-after header bare seconds",
			text:    "Retry-After: 60",
			wantOK:  true,
			wantUTC: now.Add(60 * time.Second),
		},
		{
			name:   "ambiguous bare at-number rejected",
			text:   "look at 4 things",
			wantOK: false,
		},
		{
			name:   "no hint",
			text:   "some unrelated error",
			wantOK: false,
		},
		{
			name:   "empty",
			text:   "",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseResetTime(tc.text, now)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v (got time %v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if !got.Equal(tc.wantUTC) {
				t.Fatalf("reset = %v, want %v", got, tc.wantUTC)
			}
		})
	}
}

// TestParseResetTime_JustPassedNotRolledToTomorrow is the regression for the
// ~24h-wait bug: re-reading "try again at 8:07 PM" a few seconds after 8:07 PM
// must report no reset (so the caller backs off and retries soon), not roll the
// reset forward a full day.
func TestParseResetTime_JustPassedNotRolledToTomorrow(t *testing.T) {
	justAfter := time.Date(2026, 6, 16, 20, 7, 3, 0, time.Local) // 8:07:03 PM
	if got, ok := ParseResetTime("You've hit your usage limit ... try again at 8:07 PM.", justAfter); ok {
		t.Fatalf("expected no reset for a just-passed time, got %v (~%s away)", got, got.Sub(justAfter))
	}
	// Sanity: the same message a couple hours BEFORE the reset is still used.
	before := time.Date(2026, 6, 16, 18, 20, 0, 0, time.Local)
	got, ok := ParseResetTime("try again at 8:07 PM.", before)
	if !ok || !got.Equal(time.Date(2026, 6, 16, 20, 7, 0, 0, time.Local)) {
		t.Fatalf("future reset should be 20:07, got ok=%v %v", ok, got)
	}
}
