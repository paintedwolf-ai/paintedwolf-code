//go:build grammar_subset && grammar_subset_dockerfile

package grammars

func init() {
	RegisterExternalScanner("dockerfile", DockerfileExternalScanner{})
}
