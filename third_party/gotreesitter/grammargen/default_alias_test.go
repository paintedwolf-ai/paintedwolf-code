package grammargen

import "testing"

func TestPromoteDefaultAlias(t *testing.T) {
	g := &Grammar{
		Name: "test_default_alias",
		Rules: map[string]*Rule{
			"source_file": Seq(Alias(Sym("_doc"), "document", true)),
			"_doc":        Sym("doc_item"),
			"doc_item":    Choice(Str("a"), Str("b")),
		},
		RuleOrder: []string{"source_file", "_doc", "doc_item"},
	}

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	docID := -1
	sourceID := -1
	for i, info := range ng.Symbols {
		switch info.Name {
		case "document":
			if info.Kind == SymbolNonterminal {
				docID = i
			}
		case "source_file":
			sourceID = i
		}
	}
	if docID < 0 {
		t.Fatal("missing promoted default alias symbol \"document\"")
	}
	if sourceID < 0 {
		t.Fatal("missing source_file symbol")
	}
	if !ng.Symbols[docID].Visible || !ng.Symbols[docID].Named {
		t.Fatalf("document symbol metadata = visible:%v named:%v, want visible/named true",
			ng.Symbols[docID].Visible, ng.Symbols[docID].Named)
	}

	foundSourceRef := false
	for _, prod := range ng.Productions {
		if prod.LHS != sourceID {
			continue
		}
		for _, rhs := range prod.RHS {
			if rhs == docID {
				foundSourceRef = true
			}
		}
		for _, ai := range prod.Aliases {
			if ai.Name == "document" {
				t.Fatal("source_file kept redundant production alias after default alias promotion")
			}
		}
	}
	if !foundSourceRef {
		t.Fatal("source_file does not reference promoted document symbol")
	}
}

func TestDefaultAliasRequiresAllUsesAliased(t *testing.T) {
	g := &Grammar{
		Name: "test_default_alias_partial",
		Rules: map[string]*Rule{
			"source_file": Choice(
				Sym("_doc"),
				Alias(Sym("_doc"), "document", true),
			),
			"_doc":     Sym("doc_item"),
			"doc_item": Choice(Str("a"), Str("b")),
		},
		RuleOrder: []string{"source_file", "_doc", "doc_item"},
	}

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	hiddenDocID := -1
	for i, info := range ng.Symbols {
		if info.Name == "_doc" && info.Kind == SymbolNonterminal {
			hiddenDocID = i
			break
		}
	}
	if hiddenDocID < 0 {
		t.Fatal("hidden _doc symbol should not be promoted when it appears unaliased")
	}

	hasDocumentAlias := false
	for _, prod := range ng.Productions {
		for _, ai := range prod.Aliases {
			if ai.Name == "document" {
				hasDocumentAlias = true
			}
		}
	}
	if !hasDocumentAlias {
		t.Fatal("contextual alias should remain when the symbol also appears unaliased")
	}
}

func TestHiddenStringTokenAliasPreservesTerminalAliasSequence(t *testing.T) {
	g := NewGrammar("test_hidden_string_token_alias")
	g.Define("source_file", Seq(
		Str("a"),
		Alias(Sym("_immediate_quest"), "?", false),
	))
	g.Define("_quest", Str("?"))
	g.Define("_immediate_quest", ImmToken(Str("?")))

	lang, err := GenerateLanguage(g)
	if err != nil {
		t.Fatalf("GenerateLanguage: %v", err)
	}

	questSym := -1
	for i, name := range lang.SymbolNames {
		if name == "?" && !lang.SymbolMetadata[i].Named {
			questSym = i
			break
		}
	}
	if questSym < 0 {
		t.Fatal("missing anonymous ? symbol")
	}
	if uint32(questSym) < lang.TokenCount {
		t.Fatalf("? alias symbol = %d, tokenCount = %d; want visible alias symbol outside terminal set", questSym, lang.TokenCount)
	}
	if !lang.SymbolMetadata[questSym].Visible || lang.SymbolMetadata[questSym].Named {
		t.Fatalf("? alias metadata = visible:%v named:%v, want visible anonymous",
			lang.SymbolMetadata[questSym].Visible, lang.SymbolMetadata[questSym].Named)
	}

	for _, row := range lang.AliasSequences {
		for _, sym := range row {
			if int(sym) == questSym {
				return
			}
		}
	}
	t.Fatal("hidden immediate token alias to ? was removed from AliasSequences")
}
