package wirespec

import (
	"regexp"
	"strings"
)

// TSEnumUnions returns the string unions generated into types.ts, by name.
func TSEnumUnions(content string) map[string][]string {
	out := make(map[string][]string)
	// Generated nested enums only. `*` admits single-value enums, which
	// openapi-typescript emits as one bare string literal.
	nested := regexp.MustCompile(`(?m)^        (\w+):\s("(?:[^"]+)"(?:\s*\|\s*"(?:[^"]+)")*);`)
	for _, m := range nested.FindAllStringSubmatch(content, -1) {
		name := m[1]
		var values []string
		for _, seg := range strings.Split(m[2], "|") {
			seg = strings.TrimSpace(seg)
			seg = strings.Trim(seg, `"`)
			if seg != "" {
				values = append(values, seg)
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	return out
}
