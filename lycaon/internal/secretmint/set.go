package secretmint

import "strings"

func compileSet(vendored, defaults []string) map[string]struct{} {
	set := make(map[string]struct{}, len(vendored)+len(defaults))
	for _, w := range vendored {
		if n := normalizeValue(w); n != "" {
			set[n] = struct{}{}
		}
	}
	for _, w := range defaults {
		if n := normalizeValue(w); n != "" {
			set[n] = struct{}{}
		}
	}
	return set
}

func normalizeValue(raw string) string {
	v := strings.TrimSpace(raw)
	v = stripOneQuoteLayer(v)
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return strings.ToLower(v)
}

func stripOneQuoteLayer(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
