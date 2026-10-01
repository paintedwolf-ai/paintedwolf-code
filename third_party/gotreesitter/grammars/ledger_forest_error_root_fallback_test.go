//go:build !grammar_subset

package grammars

import (
	"os"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

const (
	ledgerRecoveredDateSuffixLockedCDigest = "d89a89fd6a1397d5ccb1c9f38271dcef632e7bd4a8d307146483f180af66e173"
	ledgerYearDirectiveLockedCDigest       = "1d4aa453808e5f85eba81c33fff5bc91c217c9ec2cf66264bff2f8e4092a563b"
)

// TestLedgerForestExperimentalErrorRootFallback proves that the explicit
// forest route declines both Ledger recovery roots before compatibility runs.
// The production fallback must retain the locked C digest.
func TestLedgerForestExperimentalErrorRootFallback(t *testing.T) {
	tests := []struct {
		name          string
		source        []byte
		lockedCDigest string
	}{
		{
			name:          "recovered-date-suffix",
			source:        []byte("\n15.03.2006 Exxon\n    Expenses:Auto:Gas          10,00 EUR\n    Liabilities:MasterCard    -10,00 EUR\n"),
			lockedCDigest: ledgerRecoveredDateSuffixLockedCDigest,
		},
		{
			name: "year-directive",
			source: []byte(`
--input-date-format %d.%m

Y2010
03.01 * Foo
    A                 10.00 EUR
    B

05.02 * Bar
    A                 20.00 EUR
    B

test reg A
10-Jan-03 Foo                   A                         10.00 EUR    10.00 EUR
10-Feb-05 Bar                   A                         20.00 EUR    30.00 EUR
end test

`),
			lockedCDigest: ledgerYearDirectiveLockedCDigest,
		},
	}

	language := LedgerLanguage()
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			productionParser := gotreesitter.NewParser(language)
			productionParser.SetAdmissionCandidateRoute(false)
			production, err := productionParser.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(production.Release)
			if production.ParseRuntime().ForestFastPath {
				t.Fatal("production parse used the forest route")
			}
			assertLedgerLockedCDigest(t, "production", production, language, test.lockedCDigest)

			forestParser := gotreesitter.NewParser(language)
			forest, ok := forestParser.ParseForestExperimental(test.source)
			if ok || forest != nil {
				if forest != nil {
					forest.Release()
				}
				t.Fatalf("forest result ok=%t tree_nil=%t, want error_root decline", ok, forest == nil)
			}
			_, _, reason, _ := forestParser.ForestDeclineInfo()
			if reason != "error_root" {
				t.Fatalf("forest decline reason=%q, want error_root", reason)
			}

			fallback, err := forestParser.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(fallback.Release)
			if fallback.ParseRuntime().ForestFastPath {
				t.Fatal("forest fallback used the forest route")
			}
			assertLedgerLockedCDigest(t, "forest-fallback", fallback, language, test.lockedCDigest)
		})
	}
}

func assertLedgerLockedCDigest(
	t *testing.T,
	route string,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
	want string,
) {
	t.Helper()
	inspection, err := benchfixtures.InspectGoTree(tree.RootNode(), language)
	if err != nil {
		t.Fatalf("inspect %s route: %v", route, err)
	}
	if inspection.SHA256 != want {
		t.Fatalf("%s digest=%s, want locked C digest %s", route, inspection.SHA256, want)
	}
	t.Logf("%s digest=%s locked C digest", route, inspection.SHA256)
}

// TestForestExperimentalCleanRootControl proves clean forest trees remain
// direct results after the error-root guard rejects recovery-only roots.
func TestForestExperimentalCleanRootControl(t *testing.T) {
	previousForest := os.Getenv("GOT_GLR_FOREST") != "0"
	gotreesitter.SetGLRForestEnabled(false)
	t.Cleanup(func() { gotreesitter.SetGLRForestEnabled(previousForest) })

	language := BashLanguage()
	source := []byte("x=1\n")
	production, err := gotreesitter.NewParser(language).Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(production.Release)

	forest, ok := gotreesitter.NewParser(language).ParseForestExperimental(source)
	if !ok || forest == nil {
		t.Fatalf("clean forest result ok=%t tree_nil=%t", ok, forest == nil)
	}
	t.Cleanup(forest.Release)
	if !forest.ParseRuntime().ForestFastPath {
		t.Fatal("clean result did not report the forest route")
	}
	if forest.RootNode().IsError() || forest.RootNode().HasErrorOrMissing() {
		t.Fatalf("clean forest root has recovery flags: %s", forest.RootNode().SExpr(language))
	}
	productionDigest, err := benchfixtures.InspectGoTree(production.RootNode(), language)
	if err != nil {
		t.Fatal(err)
	}
	forestDigest, err := benchfixtures.InspectGoTree(forest.RootNode(), language)
	if err != nil {
		t.Fatal(err)
	}
	if forestDigest.SHA256 != productionDigest.SHA256 {
		t.Fatalf("clean forest digest=%s, production=%s", forestDigest.SHA256, productionDigest.SHA256)
	}
}
