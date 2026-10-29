package observability

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

// shortSessionIDBytes bounds a session id in a log line.
const shortSessionIDBytes = 8

var (
	bearerTokenRE = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\-._~+/]+=*`)
	pemBlockRE    = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`)
	secretEnvRE   = buildSecretAssignmentRE()
	secretFlagRE  = buildSecretFlagRE()
)

// buildSecretAssignmentRE matches named values in diagnostic text.
func buildSecretAssignmentRE() *regexp.Regexp {
	alts := make([]string, 0, len(secretFieldNames))
	for _, name := range secretFieldNames {
		// Accept joined, underscored, and dashed field names.
		alts = append(alts, strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[_-]?"))
	}
	// Horizontal whitespace prevents a missing value from consuming the next line.
	horizontal := `[^\S\r\n]*`
	return regexp.MustCompile(`(?i)((?:` + strings.Join(alts, "|") + `)` + horizontal + `[=:]` + horizontal + `)\S+`)
}

// buildSecretFlagRE matches secret-bearing long options.
func buildSecretFlagRE() *regexp.Regexp {
	names := make([]string, 0, len(secretFieldNames))
	for _, name := range secretFieldNames {
		names = append(names, strings.ReplaceAll(regexp.QuoteMeta(name), "_", "[-_]?"))
	}
	// Use ASCII spacing so a missing value cannot consume the next line.
	value := `(?:"[^"]*"|'[^']*'|[^\s]+)`
	return regexp.MustCompile(`(?i)((?:--)[a-z0-9_-]*(?:` + strings.Join(names, "|") + `)[a-z0-9_-]*(?:=|[ \t]+))` + value)
}

const redacted = "[REDACTED]"

// RedactString strips bearer tokens, PEM blocks, and common secret patterns from text.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	s = bearerTokenRE.ReplaceAllString(s, "${1}"+redacted)
	s = pemBlockRE.ReplaceAllString(s, redacted)
	s = secretFlagRE.ReplaceAllString(s, "${1}"+redacted)
	s = secretEnvRE.ReplaceAllString(s, "${1}"+redacted)
	return s
}

// ShortSessionID truncates UUID session IDs in logs to reduce capability leakage.
func ShortSessionID(id string) string {
	id = strings.TrimSpace(id)
	return runeclamp.ClampBytes(id, shortSessionIDBytes)
}
