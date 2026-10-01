package litprefilter

import (
	"sync"
	"unicode"
	"unicode/utf8"
)

// CanonicalFold maps r to the least rune of its case class: the connected
// component of runes joined by unicode.SimpleFold, ToLower, ToUpper and
// ToTitle. Two runes share a class exactly when strings.ToLower or a (?i)
// regexp can equate them, so an index keyed on it is a superset for both
// (ı and İ join i, the Kelvin sign joins k).
//
// ASCII letters fold to uppercase: the least member of every ASCII class.
func CanonicalFold(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	if folded, ok := canonicalFoldTable()[r]; ok {
		return folded
	}
	return r
}

// AppendCanonicalFold appends the CanonicalFold of every rune in src to dst.
// Invalid UTF-8 bytes pass through unchanged so a caller that validates
// separately sees a length-preserving copy for them.
func AppendCanonicalFold(dst, src []byte) []byte {
	for i := 0; i < len(src); {
		c := src[i]
		if c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			dst = append(dst, c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, c)
			i++
			continue
		}
		dst = utf8.AppendRune(dst, CanonicalFold(r))
		i += size
	}
	return dst
}

// canonicalFoldTable holds the least member of every multi-rune case class,
// keyed by each non-ASCII member. Runes without a case mapping are not
// present and fold to themselves.
var canonicalFoldTable = sync.OnceValue(func() map[rune]rune {
	parent := map[rune]rune{}
	var find func(r rune) rune
	find = func(r rune) rune {
		p, ok := parent[r]
		if !ok || p == r {
			return r
		}
		root := find(p)
		parent[r] = root
		return root
	}
	union := func(a, b rune) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra > rb {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		if _, ok := parent[ra]; !ok {
			parent[ra] = ra
		}
	}
	// Every rune with a case relation lies in CaseRanges; SimpleFold orbits
	// (ſ, K, Ω …) are made of runes that also carry a case mapping.
	for _, cr := range unicode.CaseRanges {
		for r := rune(cr.Lo); r <= rune(cr.Hi); r++ {
			for _, related := range [...]rune{unicode.SimpleFold(r), unicode.ToLower(r), unicode.ToUpper(r), unicode.ToTitle(r)} {
				if related != r {
					union(r, related)
				}
			}
		}
	}
	least := map[rune]rune{}
	for r := range parent {
		root := find(r)
		if current, ok := least[root]; !ok || r < current {
			least[root] = r
		}
	}
	out := make(map[rune]rune, len(parent))
	for r := range parent {
		if r >= utf8.RuneSelf {
			out[r] = least[find(r)]
		}
	}
	return out
})
