package theme

import (
	"strings"
	"testing"
)

// Derivations may read only earlier tokens.
func TestDerivationsOnlyReadEarlierTokens(t *testing.T) {
	t.Parallel()

	for i, tok := range baseTokens {
		// Change fills are fitted after the syntax plane resolves.
		if tok.Fill == FillRequired || tok.Fill == FillChange {
			continue
		}
		if tok.derive == nil {
			t.Errorf("%s is %s with no derivation", tok.ID, tok.Fill)
			continue
		}
		earlier := map[string]bool{}
		for _, prior := range baseTokens[:i] {
			earlier[prior.ID] = true
		}
		var read []string
		for _, appearance := range Appearances() {
			tok.derive(func(id string) Color {
				read = append(read, id)
				return Color{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
			}, appearance)
		}
		for _, id := range read {
			if _, known := BaseToken(id); !known {
				t.Errorf("%s derives from unknown token %s", tok.ID, id)
				continue
			}
			if !earlier[id] {
				t.Errorf("%s derives from %s, which is declared later — it would read the zero color",
					tok.ID, id)
			}
		}
	}
}

// Required tokens and change fills carry no per-token derivation.
func TestRequiredTokensHaveNoDerivation(t *testing.T) {
	t.Parallel()

	for _, tok := range baseTokens {
		if (tok.Fill == FillRequired || tok.Fill == FillChange) && tok.derive != nil {
			t.Errorf("%s is %s but carries a derivation", tok.ID, tok.Fill)
		}
	}
}

func TestTokenIdentitiesAreUnique(t *testing.T) {
	t.Parallel()

	ids, vars := map[string]bool{}, map[string]bool{}
	for _, tok := range baseTokens {
		if ids[tok.ID] {
			t.Errorf("duplicate token id %s", tok.ID)
		}
		if vars[tok.CSSVar] {
			t.Errorf("duplicate custom property %s", tok.CSSVar)
		}
		ids[tok.ID], vars[tok.CSSVar] = true, true
		if tok.Description == "" {
			t.Errorf("%s has no description; the generated schema and docs read it", tok.ID)
		}
	}
	for _, scope := range syntaxScopes {
		if ids[scope.ID] {
			t.Errorf("syntax scope %s collides with a token id", scope.ID)
		}
		if vars[scope.CSSVar] {
			t.Errorf("duplicate custom property %s", scope.CSSVar)
		}
		ids[scope.ID], vars[scope.CSSVar] = true, true
	}
}

// Syntax folds follow parent-first declaration order.
func TestSyntaxFoldIsWellFormed(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, scope := range syntaxScopes {
		if scope.Parent == "" {
			if strings.Contains(scope.ID, ".") {
				t.Errorf("%s is dotted but folds to body text", scope.ID)
			}
			seen[scope.ID] = true
			continue
		}
		if !seen[scope.Parent] {
			t.Errorf("%s folds to %s, which is not declared before it", scope.ID, scope.Parent)
		}
		if want := scope.ID[:strings.LastIndex(scope.ID, ".")]; want != scope.Parent {
			t.Errorf("%s folds to %s, not to its own parent %s", scope.ID, scope.Parent, want)
		}
		seen[scope.ID] = true
	}
	for _, scope := range syntaxScopes {
		if len(scope.Tags) == 0 {
			t.Errorf("%s paints no Lezer tag", scope.ID)
		}
	}
}

func TestLegibilityFloorNamesRealTokens(t *testing.T) {
	t.Parallel()

	for _, pair := range legibilityFloor {
		if _, ok := BaseToken(pair.Foreground); !ok {
			t.Errorf("floor pair names unknown foreground %s", pair.Foreground)
		}
		if _, ok := BaseToken(pair.Background); !ok {
			t.Errorf("floor pair names unknown background %s", pair.Background)
		}
		if pair.Min <= 1 {
			t.Errorf("floor pair %s/%s has a meaningless minimum %.2f",
				pair.Foreground, pair.Background, pair.Min)
		}
		if pair.Why == "" {
			t.Errorf("floor pair %s/%s has no reason; it reaches the author's diagnostic",
				pair.Foreground, pair.Background)
		}
	}
}
