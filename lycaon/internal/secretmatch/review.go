package secretmatch

import "context"

// ReviewValue captures one observed range without putting bytes into Match.
func ReviewValue(text string, match Match) string {
	if match.NonDisclosable || IsManagedRule(match.RuleID) {
		return ""
	}
	runes := []rune(text)
	if match.Start < 0 || match.End <= match.Start || match.End > len(runes) {
		return ""
	}
	return string(runes[match.Start:match.End])
}

// ReviewMatches checks the captured bytes against the screened identity.
func (m *Matcher) ReviewMatches(ctx context.Context, value string, fingerprint SecretFingerprint) bool {
	return m != nil && m.fingerprinter != nil && value != "" && m.fingerprinter.Fingerprint(value) == fingerprint && !m.Protected(ctx, value)
}
