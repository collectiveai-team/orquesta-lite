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
			name:    "clock already past rolls to next day",
			text:    "try again at 9:00 AM",
			wantOK:  true,
			wantUTC: time.Date(2026, 6, 17, 9, 0, 0, 0, time.Local),
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
