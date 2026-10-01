package packboard

import (
	"fmt"
	"time"
)

// FormatRelativeDuration formats recency from t relative to now (e.g. "3m ago", "just now").
func FormatRelativeDuration(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// FormatRelativeSince returns duration since t (e.g. "started 8m ago").
func FormatRelativeSince(now, t time.Time) string {
	rel := FormatRelativeDuration(now, t)
	if rel == "" {
		return ""
	}
	if rel == "just now" {
		return "started just now"
	}
	return "started " + rel
}
