//go:build grammar_subset && grammar_subset_devicetree

package grammars

func init() {
	Register(LangEntry{
		Name:           "devicetree",
		Extensions:     []string{".dts", ".dtsi"},
		Language:       DevicetreeLanguage,
		GrammarSource:  GrammarSourceTS2GoBlob,
		HighlightQuery: "[\n    \"/delete-node/\"\n    \"/delete-property/\"\n    \"/dts-v1/\"\n    \"/incbin/\"\n    \"/include/\"\n    \"/memreserve/\"\n    \"/omit-if-no-ref/\"\n    \"#define\"\n    \"#undef\"\n    \"#include\"\n    \"#if\"\n    \"#elif\"\n    \"#else\"\n    \"#endif\"\n    \"#ifdef\"\n    \"#ifndef\"\n] @keyword\n\n[\n    \"!\"\n    \"~\"\n    \"-\"\n    \"+\"\n    \"*\"\n    \"/\"\n    \"%\"\n    \"||\"\n    \"&&\"\n    \"|\"\n    \"^\"\n    \"&\"\n    \"==\"\n    \"!=\"\n    \">\"\n    \">=\"\n    \"<=\"\n    \">\"\n    \"<<\"\n    \">>\"\n] @operator\n\n[\n    \",\"\n    \";\"\n] @punctuation.delimiter\n\n[\n    \"(\"\n    \")\"\n    \"{\"\n    \"}\"\n    \"<\"\n    \">\"\n] @punctuation.bracket\n\n(call_expression\n    function: (identifier) @function)\n\n(node\n    label: (identifier) @label)\n\n(property\n    label: (identifier) @label)\n\n(memory_reservation\n    label: (identifier) @label)\n\n(property\n    name: (identifier) @property)\n\n(identifier) @variable\n\n(unit_address) @tag\n\n(reference) @constant\n\n(comment) @comment",
	})
}
