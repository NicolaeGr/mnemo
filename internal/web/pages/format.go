package pages

import (
	"fmt"
	"time"
)

// relativeTime renders a timestamp the way a dashboard wants it: coarse and
// human ("3h ago"), degrading to a date once it is old enough to stop caring.
func relativeTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// staleAfter is how long a device can go without syncing before the UI flags it.
const staleAfter = 7 * 24 * time.Hour

// isStale reports whether a device should be shown as out of date. A device that
// has never synced is always stale.
func isStale(t *time.Time) bool {
	return t == nil || time.Since(*t) > staleAfter
}

// plusMore renders a "+N" suffix for collapsed secondary values (extra phones,
// emails), or "" when there is nothing more to show.
func plusMore(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("+%d", n)
}
