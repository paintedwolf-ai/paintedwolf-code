package secretmint

import (
	"strings"
	"unicode"
)

// splitKeySegments preserves acronym boundaries: serviceAPIKey becomes service, api, key.
func splitKeySegments(key string) []string {
	runes := []rune(key)
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for idx, r := range runes {
		if isKeySeparator(r) {
			flush()
			continue
		}
		if idx > 0 && startsSegment(runes, idx) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func isKeySeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// startsSegment separates camelCase words and the final capital of an acronym.
func startsSegment(runes []rune, idx int) bool {
	r := runes[idx]
	if !unicode.IsUpper(r) {
		return false
	}
	prev := runes[idx-1]
	if unicode.IsLower(prev) || unicode.IsDigit(prev) {
		return true
	}
	return unicode.IsUpper(prev) && idx+1 < len(runes) && unicode.IsLower(runes[idx+1])
}

// credentialSlot checks exclusions before explicit keys and trailing credential terms.
func (i *Inspector) credentialSlot(key string, enumerated map[string]string) (string, bool) {
	if key == "" || i.excludedKeys[strings.Join(splitKeySegments(key), "_")] {
		return "", false
	}
	if canon, ok := enumerated[strings.ToLower(key)]; ok {
		return canon, true
	}
	segments := splitKeySegments(key)
	if len(segments) == 0 {
		return "", false
	}
	if _, ok := i.keyTerms[segments[len(segments)-1]]; ok {
		return key, true
	}
	if len(segments) >= 2 {
		tail := [2]string{segments[len(segments)-2], segments[len(segments)-1]}
		if _, ok := i.keyTermPairs[tail]; ok {
			return key, true
		}
	}
	return "", false
}
