//go:build grammar_subset && grammar_subset_python

package grammars

func init() {
	RegisterExternalScanner("python", PythonExternalScanner{})
	RegisterExternalLexStates("python", pythonExternalLexStates)
}
