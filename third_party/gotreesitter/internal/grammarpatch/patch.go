// Package grammarpatch declares pinned upstream grammar overlays.
package grammarpatch

import "strings"

// Spec describes one checked-in grammar source overlay.
type Spec struct {
	File             string
	RegenerateParser bool
}

var byLanguage = map[string]Spec{
	"typescript": {
		File:             "tree-sitter-typescript-import-type.patch",
		RegenerateParser: true,
	},
	"tsx": {
		File:             "tree-sitter-typescript-import-type.patch",
		RegenerateParser: true,
	},
	"yaml": {
		File: "tree-sitter-yaml-multiline-quoted-scalars.patch",
	},
}

// Lookup returns the overlay specification for a grammar.
func Lookup(name string) (Spec, bool) {
	spec, ok := byLanguage[strings.ToLower(strings.TrimSpace(name))]
	return spec, ok
}
