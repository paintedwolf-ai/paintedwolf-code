package archlint_test

import (
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/lycaon/lycaon/internal/archlint"
	"github.com/lycaon/lycaon/internal/archlint/analyzers"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVetKeysFindingsBySymbolAndAnalyzer(t *testing.T) {
	got, err := archlint.Vet("testdata/vetmod", []string{"./..."}, analyzers.All())
	testutil.FailErr(t, "archlint.Vet failed", err)
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want one forward", got)
	}
	f := got[0]
	if f.Kind != "reexport" || f.Key != "example.com/vetmod/a: Lower forwards to strings.ToLower" || f.Pos != "a/a.go:5:6" {
		t.Fatalf("finding = %+v", f)
	}
	if _, err := archlint.Vet("testdata/missing", []string{"./..."}, analyzers.All()); err == nil {
		t.Fatal("an unloadable directory must fail")
	}
	if _, err := archlint.Vet("testdata/vetbroken", []string{"./..."}, analyzers.All()); err == nil {
		t.Fatal("a package that does not type-check must fail")
	}
	if _, err := archlint.Vet("testdata/vetmod", []string{"./..."}, []*analysis.Analyzer{{Name: "invalid", Doc: "has no Run"}}); err == nil {
		t.Fatal("an invalid analyzer must fail")
	}
}
