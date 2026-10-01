//go:build grammar_subset && grammar_subset_cuda

package grammars

func init() {
	Register(LangEntry{
		Name:           "cuda",
		Extensions:     []string{".cu", ".cuh"},
		Language:       CudaLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "; inherits: cpp\n\n[\n  \"<<<\"\n  \">>>\"\n] @punctuation.bracket\n\n[\n  \"__host__\"\n  \"__device__\"\n  \"__global__\"\n  \"__managed__\"\n  \"__forceinline__\"\n  \"__noinline__\"\n] @keyword.modifier\n\n\"__launch_bounds__\" @keyword.modifier\n",
	})
}
