//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

func TestBashCommandNameConcatenationNeedsNoResultCompatibility(t *testing.T) {
	source := []byte("${0%/*}/check-unsafe-assertions.sh")
	language := BashLanguage()

	rawParser := gotreesitter.NewParser(language)
	rawParser.SetAdmissionCandidateRoute(false)
	raw, err := rawParser.ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(raw.Release)

	productionParser := gotreesitter.NewParser(language)
	productionParser.SetAdmissionCandidateRoute(false)
	production, err := productionParser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(production.Release)

	const wantDigest = "a5b4479ddfaa21239153effb0ee7b1f886155c25e2ea6ba43d56c5087a211819"
	if got := bashCommandNameTreeDigest(t, raw, language); got != wantDigest {
		t.Fatalf("raw digest = %s, want %s", got, wantDigest)
	}
	if got := bashCommandNameTreeDigest(t, production, language); got != wantDigest {
		t.Fatalf("production digest = %s, want %s", got, wantDigest)
	}
}

func TestBashCommandNameConcatenationRoutes(t *testing.T) {
	gotreesitter.SetGLRForestEnabled(false)
	t.Cleanup(func() { gotreesitter.SetGLRForestEnabled(true) })

	receipts := retiredDispatchRouteReceiptsAllowCompactFallback(
		t,
		BashLanguage(),
		[]byte("${0%/*}/check-unsafe-assertions.sh"),
	)
	for _, receipt := range receipts {
		if receipt.name != "incremental" {
			continue
		}
		if receipt.incrementalProfile.ReuseUnsupported {
			if receipt.incrementalProfile.ReuseUnsupportedReason == "" {
				t.Fatalf("incremental reuse status = %+v", receipt.incrementalProfile)
			}
			return
		}
		if !receipt.incrementalProfile.OldTreeReuseRoute ||
			receipt.incrementalProfile.ReusedSubtrees == 0 ||
			receipt.incrementalProfile.ReusedBytes == 0 {
			t.Fatalf("incremental route did not reuse the old tree: %+v", receipt.incrementalProfile)
		}
	}
}

func bashCommandNameTreeDigest(
	t *testing.T,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
) string {
	t.Helper()
	inspection, err := benchfixtures.InspectGoTree(tree.RootNode(), language)
	if err != nil {
		t.Fatal(err)
	}
	return inspection.SHA256
}
