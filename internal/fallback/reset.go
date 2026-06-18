package fallback

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// reRelative matches relative reset hints such as "try again in 30 seconds",
// "retry after 5 minutes", "retry-after: 60" (a bare number is seconds).
var reRelative = regexp.MustCompile(`(?i)(?:try again in|retry after|retry-after:?|retry in|resets? in|available in|wait)\s*(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)?`)

// reClock matches absolute clock hints such as "try again at 4:30 PM",
// "at 16:30", "at 4 PM", or "resets 7pm" / "resets at 7 pm" (Claude's session
// limit phrases it as "resets 7pm"). The lead-in is "at" or "reset[s]"
// (optionally "reset at"). A bare "at 4" / "resets 7" (no minutes and no am/pm)
// is too ambiguous and is rejected by ParseResetTime.
var reClock = regexp.MustCompile(`(?i)(?:\bat|\breset(?:s)?(?:\s+at)?)\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?`)

// ParseResetTime extracts a rate-limit reset time from agent output, relative
// to now. It recognises relative hints ("try again in 30 seconds") and
// absolute clock hints ("try again at 4:30 PM", interpreted in now's location
// and rolled to the next day when already past). Returns ok=false when no
// usable hint is found, in which case callers fall back to exponential backoff.
func ParseResetTime(text string, now time.Time) (time.Time, bool) {
	if text == "" {
		return time.Time{}, false
	}

	if m := reRelative.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			unit := strings.ToLower(m[2])
			var d time.Duration
			switch {
			case strings.HasPrefix(unit, "h"):
				d = time.Duration(n) * time.Hour
			case strings.HasPrefix(unit, "m"):
				d = time.Duration(n) * time.Minute
			default: // seconds, or a bare number (Retry-After header style)
				d = time.Duration(n) * time.Second
			}
			return now.Add(d), true
		}
	}

	if m := reClock.FindStringSubmatch(text); m != nil {
		hasMinutes := m[2] != ""
		ap := strings.ToLower(m[3])
		// "at 4" alone is too ambiguous to be a reliable reset time.
		if !hasMinutes && ap == "" {
			return time.Time{}, false
		}
		hour, err := strconv.Atoi(m[1])
		if err != nil || hour < 0 || hour > 23 {
			return time.Time{}, false
		}
		minute := 0
		if hasMinutes {
			minute, _ = strconv.Atoi(m[2])
		}
		switch ap {
		case "pm":
			if hour < 12 {
				hour += 12
			}
		case "am":
			if hour == 12 {
				hour = 0
			}
		}
		if hour > 23 || minute < 0 || minute > 59 {
			return time.Time{}, false
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return t, true
	}

	return time.Time{}, false
}
