package slash

import (
	"regexp"
	"strings"
)

var triggerPattern = regexp.MustCompile(`^/([\w-]+)`)

// ParseCommand returns the leading /word trigger token if present.
func ParseCommand(text string) (trigger string, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '/' {
		return "", false
	}
	m := triggerPattern.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", false
	}
	return "/" + m[1], true
}

// Remainder is the text after a leading /trigger, or empty.
func Remainder(text string) string {
	trimmed := strings.TrimSpace(text)
	trigger, ok := ParseCommand(trimmed)
	if !ok {
		return ""
	}
	return strings.TrimSpace(trimmed[len(trigger):])
}
