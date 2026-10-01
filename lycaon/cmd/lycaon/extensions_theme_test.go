package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/theme"
)

// Every scaffold form compiles before editing.
func TestScaffoldOutputCompiles(t *testing.T) {
	t.Parallel()

	root, err := moduleRootForThemes()
	if err != nil {
		t.Skipf("not inside the repository: %v", err)
	}
	for _, stock := range []string{"daylight", "charcoal"} {
		base, err := compileStockTheme(root, stock)
		if err != nil {
			t.Fatalf("compile stock %s: %v", stock, err)
		}
		for _, full := range []bool{false, true} {
			name := stock
			if full {
				name += "-full"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				body := themeScaffold("acme/fork:"+name, "Fork", base, full)
				decl, err := contribution.DecodeTheme([]byte(body))
				if err != nil {
					t.Fatalf("scaffold does not decode: %v\n%s", err, body)
				}
				compiled, err := decl.Compiled()
				if err != nil {
					t.Fatalf("scaffold does not compile: %v\n%s", err, body)
				}
				if compiled.Appearance != base.Appearance {
					t.Errorf("appearance = %s, want %s", compiled.Appearance, base.Appearance)
				}
				if !reflect.DeepEqual(compiled.WindowColors, base.WindowColors) {
					t.Errorf("window colors = %+v, forked from %+v", compiled.WindowColors, base.WindowColors)
				}
				if compiled.Bevel != base.Bevel {
					t.Errorf("bevel = %+v, forked from %+v", compiled.Bevel, base.Bevel)
				}
				if full && !strings.Contains(body, "\nbevel:\n  strength: ") {
					t.Errorf("full scaffold does not name bevel.strength\n%s", body)
				}
				// The terse form reproduces omitted values through derivation.
				for _, tok := range theme.BaseTokens() {
					if got, want := compiled.Tokens[tok.ID], base.Tokens[tok.ID]; got != want {
						t.Errorf("token %s = %s, forked from %s", tok.ID, got, want)
					}
				}
				for _, scope := range theme.SyntaxScopes() {
					got, want := compiled.Syntax[scope.ID], base.Syntax[scope.ID]
					if got.Color != want.Color || got.Italic != want.Italic ||
						got.Bold != want.Bold || got.Underline != want.Underline {
						t.Errorf("scope %s = %+v, forked from %+v", scope.ID, got, want)
					}
				}
			})
		}
	}
}

// The terse scaffold contains authored decisions only.
func TestTerseScaffoldListsDecisionsOnly(t *testing.T) {
	t.Parallel()

	root, err := moduleRootForThemes()
	if err != nil {
		t.Skipf("not inside the repository: %v", err)
	}
	base, err := compileStockTheme(root, "charcoal")
	if err != nil {
		t.Fatalf("compile stock: %v", err)
	}
	body := themeScaffold("acme/fork:terse", "Fork", base, false)
	baseline, ok := derivedBaseline(base)
	if !ok {
		t.Fatal("stock theme has no derived baseline")
	}

	listed := func(id string) bool { return strings.Contains(body, "\n  "+id+":") }
	for _, tok := range theme.BaseTokens() {
		switch {
		case tok.Fill == theme.FillBevel || tok.Fill == theme.FillChange:
			if listed(tok.ID) {
				t.Errorf("host-derived token %s is listed; a theme cannot author it", tok.ID)
			}
		case tok.Fill == theme.FillRequired:
			if !listed(tok.ID) {
				t.Errorf("required token %s is missing", tok.ID)
			}
		case baseline.Tokens[tok.ID] == base.Tokens[tok.ID]:
			if listed(tok.ID) {
				t.Errorf("token %s derives back to its value; leave it out", tok.ID)
			}
		default:
			if !listed(tok.ID) {
				t.Errorf("token %s does not derive back to %s; dropping it changes the fork",
					tok.ID, base.Tokens[tok.ID])
			}
		}
	}
	for _, scope := range theme.SyntaxScopes() {
		if scope.Parent == "" {
			continue
		}
		if baseline.Syntax[scope.ID] == base.Syntax[scope.ID] {
			if listed(scope.ID) {
				t.Errorf("scope %s folds to its value; leave it out", scope.ID)
			}
		} else if !listed(scope.ID) {
			t.Errorf("scope %s does not fold to %v; dropping it changes the fork",
				scope.ID, base.Syntax[scope.ID].Color)
		}
	}

	if lines := strings.Count(body, "\n"); lines > 98 {
		t.Errorf("terse scaffold is %d lines; the derivations are not earning their keep", lines)
	}
}
