package toolvocab_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

func TestBlocksSplitParagraphsListItemsAndSkipFences(t *testing.T) {
	text := "Lead line\ncontinues.\n\n- item one\n  wraps\n2. item two\n# Heading\n| a | b |\n```\ncode `x(`\n```\ntail"
	got := toolvocab.Blocks(text)
	want := []toolvocab.Block{
		{Text: "Lead line\ncontinues.", Line: 1},
		{Text: "- item one\n  wraps", Line: 4},
		{Text: "2. item two", Line: 6},
		{Text: "# Heading", Line: 7},
		{Text: "| a | b |", Line: 8},
		{Text: "tail", Line: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Blocks = %#v, want %#v", got, want)
	}
}

func TestUnheldCallsNeedAnEarlierExactLoaderInTheBlock(t *testing.T) {
	cat, err := toolvocab.NewCatalog(&toolschema.Config{}, []sandbox.ToolProfile{
		{ID: "loader", Tools: map[string]bool{"request_tools": true, "recall": true, "source_history": true}},
	}, toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	offered := map[string]bool{"request_tools": true, "recall": true}
	requestable := []string{"source_history"}
	for text, unheld := range map[string]bool{
		"Call `request_tools(need=\"source_history\")`, then `source_history(path=\"a\")`.":    false,
		"Call `request_tools(need: \"source_history\")` then `source_history(path=\"a(b)\")`.": false,
		"Mention `source_history` and call `recall(query=\"x\")`.":                             false,
		"Call `source_history(path=\"a\")`.":                                                   true,
		"Call `source_history(path=\"a\")` after `request_tools(need=\"source_history\")`.":    true,
		"Call `request_tools(need=\"history of a file\")`, then `source_history(path=\"a\")`.": true,
		"Call `request_tools(need=\"recall\")`, then `source_history(path=\"a\")`.":            true,
		"`request_tools(need=\"source_history\")`\n\n`source_history(path=\"a\")`":             true,
		"- `request_tools(need=\"source_history\")`\n- `source_history(path=\"a\")`":           true,
	} {
		if got := len(toolvocab.UnheldCalls(cat, text, offered, requestable)) > 0; got != unheld {
			t.Errorf("%q: unheld=%t, want %t", text, got, unheld)
		}
	}
}
