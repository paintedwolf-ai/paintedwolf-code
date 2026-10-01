//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

const (
	bashGeneratedCommandAssignmentSource = "zipname=npm-$(node ../cli.js -v).zip"
	bashGeneratedCommandAssignmentDigest = "a2dac68ad605a69720f13549488b1519627653226bb252bd89db912ae1558e8b"
)

func TestBashGeneratedCommandAssignmentNeedsNoResultCompatibility(t *testing.T) {
	language := BashLanguage()
	source := []byte(bashGeneratedCommandAssignmentSource)

	rawParser := gotreesitter.NewParser(language)
	rawParser.SetAdmissionCandidateRoute(false)
	raw, err := rawParser.ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(raw.Release)
	assertNoNormalizationPasses(t, raw)

	productionParser := gotreesitter.NewParser(language)
	productionParser.SetAdmissionCandidateRoute(false)
	production, err := productionParser.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(production.Release)
	assertNoNormalizationPasses(t, production)

	if got := bashGeneratedCommandAssignmentTreeDigest(t, raw, language); got != bashGeneratedCommandAssignmentDigest {
		t.Fatalf("raw digest = %s, want %s", got, bashGeneratedCommandAssignmentDigest)
	}
	if got := bashGeneratedCommandAssignmentTreeDigest(t, production, language); got != bashGeneratedCommandAssignmentDigest {
		t.Fatalf("production digest = %s, want %s", got, bashGeneratedCommandAssignmentDigest)
	}
}

func TestBashGeneratedCommandAssignmentRoutes(t *testing.T) {
	gotreesitter.SetGLRForestEnabled(false)
	t.Cleanup(func() { gotreesitter.SetGLRForestEnabled(true) })

	receipts := retiredDispatchRouteReceiptsAllowCompactFallbackExactSource(
		t,
		BashLanguage(),
		[]byte(bashGeneratedCommandAssignmentSource),
	)
	for _, receipt := range receipts {
		assertNoNormalizationPasses(t, receipt.tree)
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

func bashGeneratedCommandAssignmentTreeDigest(
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
