//go:build grammar_subset && grammar_subset_org

package grammars

func init() {
	RegisterExternalScanner("org", OrgExternalScanner{})
}
