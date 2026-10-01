//go:build grammar_subset && grammar_subset_cuda

package grammars

func init() {
	RegisterExternalScanner("cuda", CudaExternalScanner{})
}
