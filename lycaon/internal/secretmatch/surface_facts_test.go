package secretmatch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Every surface requires presentation facts.
func TestEveryDeclaredSurfaceHasFacts(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ask.go", nil, 0)
	testutil.FailErr(t, "parse ask.go", err)

	declared := map[ScreenSurface]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		typeName, _ := spec.Type.(*ast.Ident)
		if typeName == nil || typeName.Name != "ScreenSurface" {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		declared[ScreenSurface(value)] = spec.Names[0].Name
		return true
	})
	if len(declared) == 0 {
		t.Fatal("no ScreenSurface constants found; the scan stopped seeing its subject")
	}
	for surface, constName := range declared {
		if _, ok := surfaceFacts[surface]; !ok {
			t.Errorf("%s (%q) has no entry in surfaceFacts", constName, surface)
		}
	}
	for surface := range surfaceFacts {
		if _, ok := declared[surface]; !ok {
			t.Errorf("surfaceFacts names %q, which is not a declared surface", surface)
		}
	}
}

// Redaction support and its explanation are mutually exclusive.
func TestSurfaceRedactionIsExactlyOneAnswer(t *testing.T) {
	for surface := range surfaceFacts {
		if strings.TrimSpace(surface.Label()) == "" {
			t.Errorf("%q has no label", surface)
		}
		note := strings.TrimSpace(surface.RedactionNote())
		if surface.CanRedact() != (note == "") {
			t.Errorf("%q: CanRedact=%v note=%q", surface, surface.CanRedact(), note)
		}
	}
}

// Unknown surfaces default to non-redactable.
func TestUnknownSurfaceFallsBackToStatedNoRedaction(t *testing.T) {
	unknown := ScreenSurface("surface_nobody_declared")
	if unknown.CanRedact() {
		t.Error("an unlisted surface claimed rewrite safety")
	}
	if strings.TrimSpace(unknown.RedactionNote()) == "" {
		t.Error("an unlisted surface dropped redaction without saying why")
	}
	if unknown.Label() != string(unknown) {
		t.Errorf("label = %q, want the identifier itself", unknown.Label())
	}
}
