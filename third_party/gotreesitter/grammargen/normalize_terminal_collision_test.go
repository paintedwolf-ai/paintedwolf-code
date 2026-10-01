package grammargen

import "testing"

func TestEscapeAnonymousNameDecodesUnicodeEscapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "fixed arrow", in: `\u2192`, want: "→"},
		{name: "braced lambda", in: `\u{03BB}`, want: "λ"},
		{name: "decoded question uses internal escape", in: `\u003F`, want: `\?`},
		{name: "literal question uses internal escape", in: "?", want: `\?`},
		{name: "literal backslash question stays distinct", in: `\?`, want: `\\?`},
		{name: "surrogate pair", in: `\uD83D\uDE00`, want: "😀"},
		{name: "invalid fixed escape", in: `\uZZZZ`, want: `\uZZZZ`},
		{name: "incomplete fixed escape", in: `\u219`, want: `\u219`},
		{name: "unpaired high surrogate", in: `\uD83D`, want: `\uD83D`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeAnonymousName(tt.in); got != tt.want {
				t.Fatalf("escapeAnonymousName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeUsesDecodedUnicodeAnonymousDisplayName(t *testing.T) {
	g := NewGrammar("unicode_anonymous_display")
	g.Define("source_file", Seq(
		Str(`\u2192`),
		Str(`\u003F`),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	foundArrow := false
	foundQuestion := false
	for _, sym := range ng.Symbols {
		switch sym.Name {
		case "→":
			foundArrow = true
		case `\?`:
			foundQuestion = true
		case `\u2192`, `\u003F`:
			t.Fatalf("found undecoded anonymous display name %q", sym.Name)
		}
	}
	if !foundArrow {
		t.Fatal("missing decoded arrow anonymous display name")
	}
	if !foundQuestion {
		t.Fatal("missing decoded+escaped question anonymous display name")
	}
}

func TestNormalizeSeparatesAnonymousStringAndPatternTerminals(t *testing.T) {
	g := NewGrammar("terminal_collision")
	g.Define("source_file", Seq(
		Choice(
			Str(".*"),
			Pat(`.*`),
		),
		Str(";"),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	var stringSymID int = -1
	collisionSymIDs := map[int]struct{}{}
	for _, term := range ng.Terminals {
		if term.Rule == nil {
			continue
		}
		if ng.Symbols[term.SymbolID].Name == ".*" {
			collisionSymIDs[term.SymbolID] = struct{}{}
		}
		if term.Rule.Kind == RuleString && term.Rule.Value == ".*" {
			stringSymID = term.SymbolID
		}
	}

	if stringSymID < 0 {
		t.Fatal("missing anonymous string terminal for \".*\"")
	}
	if ng.Symbols[stringSymID].Name != ".*" {
		t.Fatalf("string terminal display name = %q, want %q", ng.Symbols[stringSymID].Name, ".*")
	}
	if len(collisionSymIDs) != 2 {
		t.Fatalf("expected 2 distinct terminals named %q, got %d", ".*", len(collisionSymIDs))
	}
}

func TestNormalizeWrapsSharedStringOnlyNamedTokenWithAnonymousLiteral(t *testing.T) {
	g := NewGrammar("string_token_collision")
	g.Define("source_file", Choice(
		Str("import"),
		Sym("import"),
	))
	g.Define("import", Token(Str("import")))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	anonID := -1
	importID := -1
	sourceID := -1
	for id, sym := range ng.Symbols {
		switch {
		case sym.Name == "source_file":
			sourceID = id
		case sym.Name == "import" && sym.Kind == SymbolTerminal && !sym.Named:
			anonID = id
		case sym.Name == "import" && sym.Kind == SymbolNonterminal && sym.Named:
			importID = id
		case sym.Name == "import" && sym.Kind == SymbolNamedToken:
			t.Fatalf("shared string-only token was registered as duplicate terminal: symbol %d", id)
		}
	}
	if anonID < 0 {
		t.Fatal("missing anonymous literal terminal for import")
	}
	if importID < 0 {
		t.Fatal("missing import wrapper nonterminal")
	}

	hasImportWrapperProduction := false
	for _, prod := range ng.Productions {
		if prod.LHS == importID && len(prod.RHS) == 1 && prod.RHS[0] == anonID {
			hasImportWrapperProduction = true
			break
		}
	}
	if !hasImportWrapperProduction {
		t.Fatal("missing import wrapper production over anonymous literal")
	}

	hasAnonProduction := false
	hasImportProduction := false
	for _, prod := range ng.Productions {
		if prod.LHS != sourceID || len(prod.RHS) != 1 {
			continue
		}
		switch prod.RHS[0] {
		case anonID:
			hasAnonProduction = true
		case importID:
			hasImportProduction = true
		}
	}
	if !hasAnonProduction || !hasImportProduction {
		t.Fatalf("source_file productions missing literal/wrapper refs: anonymous=%v import=%v", hasAnonProduction, hasImportProduction)
	}
}

func TestPriorityInlinePatternsPrecedeAliasedInlinePatterns(t *testing.T) {
	g := NewGrammar("priority_inline_patterns")
	g.PriorityInlinePatterns = []string{`[0-9]+`}
	g.Define("source_file", Choice(
		Alias(Pat(`[A-Za-z0-9_]+`), "variable_name", true),
		Pat(`[0-9]+`),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	numberOrder := -1
	variableOrder := -1
	for i, term := range ng.Terminals {
		name := ng.Symbols[term.SymbolID].Name
		switch name {
		case `[0-9]+`:
			numberOrder = i
		case "variable_name":
			variableOrder = i
		}
	}
	if numberOrder < 0 {
		t.Fatal("missing priority inline number terminal")
	}
	if variableOrder < 0 {
		t.Fatal("missing aliased variable terminal")
	}
	if numberOrder > variableOrder {
		t.Fatalf("priority inline terminal order = %d, aliased terminal order = %d; want priority first", numberOrder, variableOrder)
	}
}

func TestReusedStringTokenPreservesLexicalPrecedence(t *testing.T) {
	g := NewGrammar("reused_string_token_precedence")
	g.Define("source_file", Choice(
		Str("-"),
		Seq(Token(Prec(1, Str("-"))), Pat(`[0-9]+`)),
	))

	ng, err := Normalize(g)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	for _, term := range ng.Terminals {
		if term.Rule != nil && term.Rule.Kind == RuleString && term.Rule.Value == "-" {
			if term.Priority != -1000 {
				t.Fatalf("reused string token priority = %d, want -1000", term.Priority)
			}
			return
		}
	}
	t.Fatal("missing reused string token terminal")
}
