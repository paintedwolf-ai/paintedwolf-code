package summarize

import "strings"

// dedupeAnchors keeps the first anchor for each path and line.
func dedupeAnchors(anchors []Anchor) []Anchor {
	if len(anchors) <= 1 {
		return anchors
	}
	seen := make(map[anchorKey]bool, len(anchors))
	out := make([]Anchor, 0, len(anchors))
	for _, a := range anchors {
		if strings.TrimSpace(a.Path) == "" || a.Line <= 0 {
			continue
		}
		k := anchorKey{path: a.Path, line: a.Line}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}

type anchorKey struct {
	path string
	line int
}
