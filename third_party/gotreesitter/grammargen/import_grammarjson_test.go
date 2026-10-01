package grammargen

import (
	"slices"
	"testing"
)

func TestApplyImportGrammarShapeHintsPowerShellBinaryRepeat(t *testing.T) {
	for _, name := range []string{"d", "objc", "perl", "powershell"} {
		t.Run(name, func(t *testing.T) {
			g := NewGrammar(name)
			applyImportGrammarShapeHints(g)
			if !g.BinaryRepeatMode {
				t.Fatalf("%s import should use binary repeat mode", name)
			}
		})
	}
}

func TestApplyImportGrammarShapeHintsElixirPreciseExternalLexStates(t *testing.T) {
	g := NewGrammar("elixir")
	applyImportGrammarShapeHints(g)
	if !g.PreferPreciseExternalLexStates {
		t.Fatalf("elixir import should prefer precise external lex states")
	}
	if !g.PreferRemoteCallOperatorReduces {
		t.Fatalf("elixir import should prefer remote-call operator reduces")
	}
	if !slices.Equal(g.PreserveHiddenChoicePassthrough, []string{"_capture_expression"}) {
		t.Fatalf("elixir preserved hidden passthrough = %v, want [_capture_expression]", g.PreserveHiddenChoicePassthrough)
	}
}

func TestApplyImportGrammarPostShapeHintsPerlHeredocContent(t *testing.T) {
	g := NewGrammar("perl")
	g.Define("heredoc_content", Seq(
		Sym("_heredoc_start"),
		Repeat(Choice(
			Sym("_heredoc_middle"),
			Sym("escape_sequence"),
			Sym("_interpolations"),
			Sym("_interpolation_fallbacks"),
		)),
		Sym("heredoc_end"),
	))

	applyImportGrammarPostShapeHints(g)

	rule := g.Rules["heredoc_content"]
	if rule == nil || rule.Kind != RuleSeq || len(rule.Children) != 3 {
		t.Fatalf("heredoc_content rule = %#v, want compact seq", rule)
	}
	repeat := rule.Children[1]
	if repeat == nil || repeat.Kind != RuleRepeat || len(repeat.Children) != 1 {
		t.Fatalf("middle rule = %#v, want repeat", repeat)
	}
	choice := repeat.Children[0]
	if choice == nil || choice.Kind != RuleChoice || len(choice.Children) != 2 {
		t.Fatalf("repeat content = %#v, want compact two-way choice", choice)
	}
	if got := []string{choice.Children[0].Value, choice.Children[1].Value}; got[0] != "_heredoc_middle" || got[1] != "escape_sequence" {
		t.Fatalf("compact heredoc alternatives = %v, want [_heredoc_middle escape_sequence]", got)
	}
}

// TestImportGrammarJSONRubyHeredocBodyPreservesInterpolation guards the
// public import path against silently trading Ruby semantics for bounded
// generation. Ruby's heredoc_body is a nonterminal extra whose interpolation
// alternative re-enters _statements. The LALR extra-chain builder must preserve
// that path and still finish within a bounded state graph.
func TestImportGrammarJSONRubyHeredocBodyPreservesInterpolation(t *testing.T) {
	data := []byte(`{
		"name": "ruby",
		"rules": {
			"source_file": {"type": "SYMBOL", "name": "heredoc_body"},
			"_statements": {"type": "REPEAT", "content": {"type": "SYMBOL", "name": "statement"}},
			"statement": {"type": "PATTERN", "value": "[a-z]+"},
			"interpolation": {"type": "SEQ", "members": [
				{"type": "STRING", "value": "#{"},
				{"type": "SYMBOL", "name": "_statements"},
				{"type": "STRING", "value": "}"}
			]},
			"heredoc_body": {"type": "SEQ", "members": [
				{"type": "SYMBOL", "name": "_heredoc_body_start"},
				{"type": "REPEAT", "content": {"type": "CHOICE", "members": [
					{"type": "SYMBOL", "name": "heredoc_content"},
					{"type": "SYMBOL", "name": "interpolation"},
					{"type": "SYMBOL", "name": "escape_sequence"}
				]}},
				{"type": "SYMBOL", "name": "heredoc_end"}
			]},
			"_heredoc_body_start": {"type": "STRING", "value": "<<X"},
			"heredoc_content": {"type": "PATTERN", "value": "[^#]+"},
			"escape_sequence": {"type": "TOKEN", "content": {"type": "PATTERN", "value": "\\\\."}},
			"heredoc_end": {"type": "STRING", "value": "X"}
		},
		"extras": [{"type": "SYMBOL", "name": "heredoc_body"}],
		"conflicts": [],
		"externals": [],
		"inline": [],
		"supertypes": [],
		"precedences": []
	}`)

	g, err := ImportGrammarJSON(data)
	if err != nil {
		t.Fatalf("ImportGrammarJSON: %v", err)
	}

	rule := g.Rules["heredoc_body"]
	if rule == nil || rule.Kind != RuleSeq || len(rule.Children) != 3 {
		t.Fatalf("heredoc_body rule = %#v, want original three-part sequence", rule)
	}
	repeat := rule.Children[1]
	if repeat == nil || repeat.Kind != RuleRepeat || len(repeat.Children) != 1 {
		t.Fatalf("heredoc_body middle = %#v, want repeat", repeat)
	}
	choice := repeat.Children[0]
	if choice == nil || choice.Kind != RuleChoice || len(choice.Children) != 3 {
		t.Fatalf("heredoc_body content = %#v, want original three-way choice", choice)
	}
	got := []string{choice.Children[0].Value, choice.Children[1].Value, choice.Children[2].Value}
	want := []string{"heredoc_content", "interpolation", "escape_sequence"}
	if !slices.Equal(got, want) {
		t.Fatalf("heredoc alternatives = %v, want %v", got, want)
	}

	interpolation := g.Rules["interpolation"]
	if interpolation == nil || interpolation.Kind != RuleSeq || len(interpolation.Children) != 3 || interpolation.Children[1].Value != "_statements" {
		t.Fatalf("interpolation rule = %#v, want path through _statements", interpolation)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage(imported Ruby heredoc grammar): %v", err)
	}
	if lang.StateCount == 0 || lang.StateCount > 1000 {
		t.Fatalf("generated state count = %d, want a bounded non-zero automaton", lang.StateCount)
	}
}

// TestImportGrammarJSONCrystalHeredocBodyPreservesInterpolation protects the
// Crystal import path from the semantic shortcut that the original #213 used:
// deleting interpolation from heredoc_body. The bounded LALR extra-state
// builder must make the original grammar shape finite without changing it.
func TestImportGrammarJSONCrystalHeredocBodyPreservesInterpolation(t *testing.T) {
	data := []byte(`{
		"name": "crystal",
		"rules": {
			"source_file": {"type": "SYMBOL", "name": "heredoc_body"},
			"_expression": {"type": "CHOICE", "members": [
				{"type": "SYMBOL", "name": "identifier"},
				{"type": "SEQ", "members": [
					{"type": "SYMBOL", "name": "identifier"},
					{"type": "STRING", "value": "+"},
					{"type": "SYMBOL", "name": "_expression"}
				]}
			]},
			"identifier": {"type": "PATTERN", "value": "[a-z]+"},
			"interpolation": {"type": "SEQ", "members": [
				{"type": "STRING", "value": "#{"},
				{"type": "SYMBOL", "name": "_expression"},
				{"type": "STRING", "value": "}"}
			]},
			"heredoc_body": {"type": "SEQ", "members": [
				{"type": "SYMBOL", "name": "_heredoc_body_start"},
				{"type": "REPEAT", "content": {"type": "CHOICE", "members": [
					{"type": "SYMBOL", "name": "heredoc_content"},
					{"type": "SYMBOL", "name": "interpolation"},
					{"type": "SYMBOL", "name": "string_escape_sequence"},
					{"type": "SYMBOL", "name": "ignored_backslash"}
				]}},
				{"type": "SYMBOL", "name": "heredoc_end"}
			]},
			"_heredoc_body_start": {"type": "STRING", "value": "<<X"},
			"heredoc_content": {"type": "PATTERN", "value": "[^#\\\\]+"},
			"string_escape_sequence": {"type": "TOKEN", "content": {"type": "PATTERN", "value": "\\\\\\\\."}},
			"ignored_backslash": {"type": "STRING", "value": "\\\\"},
			"heredoc_end": {"type": "STRING", "value": "X"}
		},
		"extras": [{"type": "SYMBOL", "name": "heredoc_body"}],
		"conflicts": [],
		"externals": [],
		"inline": [],
		"supertypes": [],
		"precedences": []
	}`)

	g, err := ImportGrammarJSON(data)
	if err != nil {
		t.Fatalf("ImportGrammarJSON: %v", err)
	}

	rule := g.Rules["heredoc_body"]
	if rule == nil || rule.Kind != RuleSeq || len(rule.Children) != 3 {
		t.Fatalf("heredoc_body rule = %#v, want original three-part sequence", rule)
	}
	repeat := rule.Children[1]
	if repeat == nil || repeat.Kind != RuleRepeat || len(repeat.Children) != 1 {
		t.Fatalf("heredoc_body middle = %#v, want repeat", repeat)
	}
	choice := repeat.Children[0]
	if choice == nil || choice.Kind != RuleChoice || len(choice.Children) != 4 {
		t.Fatalf("heredoc_body content = %#v, want original four-way choice", choice)
	}
	got := []string{choice.Children[0].Value, choice.Children[1].Value, choice.Children[2].Value, choice.Children[3].Value}
	want := []string{"heredoc_content", "interpolation", "string_escape_sequence", "ignored_backslash"}
	if !slices.Equal(got, want) {
		t.Fatalf("heredoc alternatives = %v, want %v", got, want)
	}

	interpolation := g.Rules["interpolation"]
	if interpolation == nil || interpolation.Kind != RuleSeq || len(interpolation.Children) != 3 || interpolation.Children[1].Value != "_expression" {
		t.Fatalf("interpolation rule = %#v, want path through _expression", interpolation)
	}

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage(imported Crystal heredoc grammar): %v", err)
	}
	if lang.StateCount == 0 || lang.StateCount > 1000 {
		t.Fatalf("generated state count = %d, want a bounded non-zero automaton", lang.StateCount)
	}
}
