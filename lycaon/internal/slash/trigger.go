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
