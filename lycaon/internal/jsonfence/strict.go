package jsonfence

// ParseStrict decodes envelope-only assistant content. Hybrid bodies with prose
// outside a single JSON payload are rejected before decode runs.
func ParseStrict[T any](content string, tryDecode func(string) (T, bool)) (T, bool) {
	var zero T
	if tryDecode == nil {
		return zero, false
	}
	valid := func(candidate string) bool {
		_, ok := tryDecode(candidate)
		return ok
	}
	if !EnvelopeOnly(content, valid) {
		return zero, false
	}
	for _, candidate := range Candidates(content) {
		if v, ok := tryDecode(candidate); ok {
			return v, true
		}
	}
	return zero, false
}
