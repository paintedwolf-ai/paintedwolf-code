package oarcore

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
