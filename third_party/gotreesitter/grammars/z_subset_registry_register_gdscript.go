//go:build grammar_subset && grammar_subset_gdscript

package grammars

func init() {
	Register(LangEntry{
		Name:           "gdscript",
		Extensions:     []string{".gd"},
		Language:       GdscriptLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(comment) @comment\n(identifier) @variable\n(name) @variable\n(string) @string\n(integer) @number\n(float) @number.float\n[\n  (true)\n  (false)\n] @boolean\n(class_definition\n  (name) @type)\n(function_definition\n  (name) @function)\n(call\n  (identifier) @function.call)\n[\n  \"func\"\n  \"class_name\"\n  \"extends\"\n  \"if\"\n  \"elif\"\n  \"else\"\n  \"for\"\n  \"while\"\n  \"return\"\n  \"var\"\n  \"const\"\n  \"signal\"\n] @keyword\n",
	})
}
