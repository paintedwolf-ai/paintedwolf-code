package secretmatch

import "strings"

const maxOCRRepairCandidates = 4

func (c ocrConfusionCatalog) boundaryGlyph(value byte) (byte, bool) {
	if value >= 128 || isASCIIAlnum(value) {
		return 0, false
	}
	canonical, ok := c.canonical[rune(value)]
	return byte(canonical), ok && canonical < 128 && isASCIIAlnum(byte(canonical))
}

// OCRDoubletCandidates repairs over-segmented glyphs using the same reviewed
// confusion catalog as secret screening. Original recognition stays unchanged.
func OCRDoubletCandidates(s string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	catalog, err := bundledOCRConfusions()
	if err != nil {
		return nil, err
	}
	return catalog.doubletCandidates(s, min(limit, maxOCRRepairCandidates)), nil
}

func (c ocrConfusionCatalog) doubletCandidates(s string, limit int) []string {
	runes := []rune(s)
	collapses := func(i int) bool {
		if i+1 >= len(runes) || runes[i] == runes[i+1] {
			return false
		}
		first, ok := c.canonical[runes[i]]
		second, other := c.canonical[runes[i+1]]
		return ok && other && first == second
	}
	var all strings.Builder
	all.Grow(len(s))
	changed := false
	for i := 0; i < len(runes); i++ {
		if collapses(i) {
			all.WriteRune(c.canonical[runes[i]])
			i++
			changed = true
		} else {
			all.WriteRune(runes[i])
		}
	}
	if !changed {
		return nil
	}
	out := []string{all.String()}
	seen := map[string]bool{s: true, out[0]: true}
	for i := 0; i+1 < len(runes) && len(out) < limit; i++ {
		if !collapses(i) {
			continue
		}
		candidate := string(runes[:i]) + string(c.canonical[runes[i]]) + string(runes[i+2:])
		if !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}
