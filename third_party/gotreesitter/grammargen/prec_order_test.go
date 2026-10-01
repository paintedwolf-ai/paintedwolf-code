package grammargen

import "testing"

func TestPrecOrderComparesRepeatedSymbolInSharedLevel(t *testing.T) {
	table := buildPrecOrderTable([][]PrecEntry{
		{
			{IsSymbol: true, Name: "type_query"},
			{IsSymbol: true, Name: "subscript_expression"},
		},
		{
			{IsSymbol: true, Name: "type_query"},
			{IsSymbol: true, Name: "_type_query_subscript_expression"},
		},
	}, nil)

	if got := table.resolveSymbolVsSymbol("_type_query_subscript_expression", "type_query"); got >= 0 {
		t.Fatalf("_type_query_subscript_expression vs type_query = %d, want type_query to outrank in shared level", got)
	}
	if got := table.resolveSymbolVsSymbol("type_query", "_type_query_subscript_expression"); got <= 0 {
		t.Fatalf("type_query vs _type_query_subscript_expression = %d, want type_query to outrank in shared level", got)
	}
}
