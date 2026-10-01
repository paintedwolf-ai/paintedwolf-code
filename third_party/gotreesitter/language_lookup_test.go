package gotreesitter

import "testing"

func TestSymbolByNameReturnsFirstDuplicate(t *testing.T) {
	lang := &Language{
		TokenCount:  5,
		SymbolNames: []string{"end", "identifier", "identifier", "stmt", "identifier"},
	}

	sym, ok := lang.SymbolByName("identifier")
	if !ok {
		t.Fatal("expected identifier symbol")
	}
	if sym != 1 {
		t.Fatalf("expected first identifier symbol 1, got %d", sym)
	}
}

func TestSymbolByNameWildcardReturnsZero(t *testing.T) {
	lang := &Language{
		TokenCount:  2,
		SymbolNames: []string{"end", "identifier"},
	}

	sym, ok := lang.SymbolByName("_")
	if !ok {
		t.Fatal("expected wildcard symbol")
	}
	if sym != 0 {
		t.Fatalf("wildcard symbol = %d, want 0", sym)
	}
}

func TestTokenSymbolsByNameFiltersTerminals(t *testing.T) {
	lang := &Language{
		TokenCount:  3,
		SymbolNames: []string{"end", "identifier", "identifier", "identifier", "stmt"},
	}

	syms := lang.TokenSymbolsByName("identifier")
	if len(syms) != 2 {
		t.Fatalf("expected 2 token symbols, got %d", len(syms))
	}
	if syms[0] != 1 || syms[1] != 2 {
		t.Fatalf("unexpected token symbols: %v", syms)
	}
}

func TestTokenSymbolsByNameDoesNotTreatUnderscoreAsWildcard(t *testing.T) {
	lang := &Language{
		TokenCount:  3,
		SymbolNames: []string{"end", "_", "identifier", "_"},
	}

	syms := lang.TokenSymbolsByName("_")
	if len(syms) != 1 {
		t.Fatalf("expected 1 token symbol, got %d: %v", len(syms), syms)
	}
	if syms[0] != 1 {
		t.Fatalf("underscore token symbol = %d, want 1", syms[0])
	}
}
