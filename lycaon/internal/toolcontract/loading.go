package toolcontract

// LoadFamilies selects permitted family members without widening availability.
func LoadFamilies(names []string, available func(string) bool) []string {
	selected := make([]string, 0, len(names))
	for _, name := range names {
		if available(name) {
			selected = append(selected, name)
		}
	}
	expanded := ExpandFamilies(selected)
	out := expanded[:0]
	for _, name := range expanded {
		if available(name) {
			out = append(out, name)
		}
	}
	return out
}
