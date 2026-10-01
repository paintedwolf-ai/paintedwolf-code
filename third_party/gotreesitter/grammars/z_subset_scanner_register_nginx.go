//go:build grammar_subset && grammar_subset_nginx

package grammars

func init() {
	RegisterExternalScanner("nginx", NginxExternalScanner{})
}
