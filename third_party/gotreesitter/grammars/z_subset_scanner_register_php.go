//go:build grammar_subset && grammar_subset_php

package grammars

func init() {
	RegisterExternalScanner("php", PhpExternalScanner{})
}
