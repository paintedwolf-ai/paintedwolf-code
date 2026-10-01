package logoutline

import (
	"regexp"
	"strings"
)

var (
	reQuoted  = regexp.MustCompile(`"([^"\\]|\\.)*"`)
	reIPv6    = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}`)
	reIPv4    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reUUID    = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reHexBlob = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	rePath    = regexp.MustCompile(`(?:/[A-Za-z0-9._~%+-]+)+`)
	reNumber  = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
)

var templateStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "at": true, "by": true, "for": true, "from": true,
	"in": true, "of": true, "on": true, "or": true, "the": true, "to": true, "with": true,
	"accepted": true, "closed": true, "connection": true, "error": true, "failed": true,
	"id": true, "info": true, "invalid": true, "opened": true, "password": true, "request": true,
	"port": true, "publickey": true, "refused": true, "session": true, "user": true,
	"warning": true, "notice": true, "debug": true, "crit": true, "alert": true, "emerg": true,
}

// maskMessage collapses variable tokens in a log message into a stable template.
// Mask order is fixed left-to-right for determinism.
func maskMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	msg = reQuoted.ReplaceAllString(msg, "<quoted>")
	msg = reIPv6.ReplaceAllString(msg, "<ipv6>")
	msg = reIPv4.ReplaceAllString(msg, "<ipv4>")
	msg = reUUID.ReplaceAllString(msg, "<uuid>")
	msg = reHexBlob.ReplaceAllString(msg, "<hex>")
	msg = rePath.ReplaceAllString(msg, "<path>")
	msg = reNumber.ReplaceAllString(msg, "<num>")
	msg = maskVariableStrings(msg)
	msg = strings.Join(strings.Fields(msg), " ")
	return msg
}

func maskVariableStrings(msg string) string {
	parts := strings.Fields(msg)
	for i, p := range parts {
		if p == "<quoted>" || p == "<ipv6>" || p == "<ipv4>" || p == "<uuid>" ||
			p == "<hex>" || p == "<path>" || p == "<num>" || p == "<str>" {
			continue
		}
		lower := strings.ToLower(p)
		if templateStopwords[lower] {
			continue
		}
		if isVariableToken(p) {
			parts[i] = "<str>"
		}
	}
	return strings.Join(parts, " ")
}

func isVariableToken(s string) bool {
	if len(s) == 0 {
		return false
	}
	hasLetter := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return hasLetter
}
