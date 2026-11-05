package oarcore

import "strings"

// RenderCopy substitutes bindings from an untyped fact map; an absent fact
// renders as the empty string.
func RenderCopy(source string, facts map[string]any) string {
	return renderCopy(source, func(name string) value {
		if raw, ok := facts[name]; ok {
			return raw
		}
		return ""
	})
}

// UnconditionalCopyText returns the literal text a copy member renders on every
// occurrence. Text inside a conditional is left out.
func UnconditionalCopyText(source string) (string, error) {
	nodes, _, err := copyParseNodes(source, 0, map[string]bool{})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.kind == copyText {
			b.WriteString(n.value)
		}
	}
	return b.String(), nil
}
