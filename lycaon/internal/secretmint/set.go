package secretmint

import (
	"strings"
)

// VendorPin records the source and integrity of a bundled password list.
type VendorPin struct {
	ID         string   `yaml:"id"`
	Upstream   string   `yaml:"upstream"`
	Ref        string   `yaml:"ref"`
	Commit     string   `yaml:"commit"`
	TreeSHA256 string   `yaml:"tree_sha256"`
	License    string   `yaml:"license"`
	Include    []string `yaml:"include"`
}

type provenanceFile struct {
	Vendors []VendorPin `yaml:"vendors"`
}

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
