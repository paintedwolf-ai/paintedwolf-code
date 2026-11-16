package pongoplain

import "strings"

// LoopBindings returns all forloop.* selector references in source, ignoring
// comments, verbatim blocks, and string literals.
func LoopBindings(source string) []string {
	var out []string
	for _, region := range templateRegion.FindAllString(source, -1) {
		if isOpaqueRegion(region) {
			continue
		}
		for _, tok := range expressionToken.FindAllString(region, -1) {
			if isQuotedLiteral(tok) {
				continue
			}
			selector := strings.Join(strings.Fields(tok), "")
			if strings.HasPrefix(selector, "forloop.") {
				out = append(out, selector)
			}
		}
	}
	return out
}
