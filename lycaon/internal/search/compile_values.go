package search

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"strings"
	"time"
	"unicode"
)

// ftsIndexable reports whether the term has a letter or digit; FTS tokens are
// runs of those, so bare punctuation like "//" has no index entry.
func ftsIndexable(term string) bool {
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func buildFTSMatch(term string, phrase bool) string {
	term = strings.TrimSpace(term)
	if term == "" {
		return term
	}
	if !phrase && strings.ContainsAny(term, " \t") {
		parts := strings.Fields(term)
		for i, p := range parts {
			parts[i] = quoteFTSToken(p)
		}
		return strings.Join(parts, " AND ")
	}
	// A quoted query term is an FTS phrase: tokens in sequence.
	return quoteFTSToken(term)
}

func quoteFTSToken(s string) string {
	s = strings.ReplaceAll(s, `"`, "")
	return `"` + s + `"`
}

func parseVerified(value string) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "matched", "traced":
		return 1, nil
	case "0", "false", "no", "unverifiable":
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid verified value")
	}
}

func parseTimeBound(value string) (string, error) {
	value = strings.TrimSpace(value)
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return timelayout.Format(t), nil
	}
	if d, err := parseDuration(value); err == nil {
		return timelayout.Format(time.Now().UTC().Add(-d)), nil
	}
	return "", fmt.Errorf("expects a time like 2026-09-01T00:00:00Z or a window like 7d, 12h, 30m")
}

func parseDuration(value string) (time.Duration, error) {
	if len(value) < 2 {
		return 0, fmt.Errorf("invalid duration")
	}
	// Six digits bound the count well before integer overflow.
	if len(value) > 7 {
		return 0, fmt.Errorf("duration too large")
	}
	unit := value[len(value)-1]
	num := value[:len(value)-1]
	n := 0
	for _, c := range num {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid duration")
		}
		n = n*10 + int(c-'0')
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	default:
		return 0, fmt.Errorf("invalid duration unit")
	}
}
