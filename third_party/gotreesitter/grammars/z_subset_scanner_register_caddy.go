//go:build grammar_subset && grammar_subset_caddy

package grammars

func init() {
	RegisterExternalScanner("caddy", CaddyExternalScanner{})
}
