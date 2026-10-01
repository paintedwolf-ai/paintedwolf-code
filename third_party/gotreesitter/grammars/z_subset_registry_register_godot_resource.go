//go:build grammar_subset && grammar_subset_godot_resource

package grammars

func init() {
	Register(LangEntry{
		Name:           "godot_resource",
		Extensions:     []string{".tres", ".tscn"},
		Language:       GodotResourceLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "(path) @property\n(integer) @number\n",
	})
}
