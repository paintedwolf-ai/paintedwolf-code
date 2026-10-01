package contract

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/designkit"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// renderAssetCodes is the closed reject set for the hermetic project-asset
// origin. Growing or shrinking it is a wire-visible contract change.
var renderAssetCodes = []string{
	"RENDER_ASSET_DENIED",
	"RENDER_ASSET_NOT_FOUND",
	"RENDER_ASSET_TOO_LARGE",
	"RENDER_ASSET_TYPE",
}

func TestRenderAssetRejectCodesClosedSet(t *testing.T) {
	t.Parallel()
	policyDir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "policy")
	entries, err := os.ReadDir(policyDir)
	contractcheck.FailErr(t, "read policy dir", err)
	var got []string
	for _, ent := range entries {
		name := strings.TrimSuffix(ent.Name(), ".yaml")
		if strings.HasPrefix(name, "RENDER_ASSET_") {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), renderAssetCodes...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("RENDER_ASSET_* policy units = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RENDER_ASSET_* policy units = %v want %v", got, want)
		}
	}
}

func TestRenderAssetBudgetsShipped(t *testing.T) {
	t.Parallel()
	// render-budgets.yaml is the sole source of these caps; this pins the
	// shipped values so changing them is a deliberate contract change.
	loaded, err := browser.LoadRenderBudgets()
	contractcheck.FailErr(t, "LoadRenderBudgets", err)
	want := browser.RenderBudgets{
		MaxAssetBytes:     2 << 20,
		MaxRenderBytes:    8 << 20,
		MaxCatalogSamples: 8,
	}
	if loaded != want {
		t.Fatalf("shipped render budgets = %+v want %+v", loaded, want)
	}
}

func TestRenderMarkupHermeticAssetAllowlist(t *testing.T) {
	t.Parallel()
	// Only the reserved asset origin passes; every other network form stays out.
	if err := browser.ValidateMarkup(`<img src="http://lycaon.asset/assets/logo.png">`, "html"); err != nil {
		t.Fatalf("asset origin img rejected: %v", err)
	}
	if err := browser.ValidateMarkup(`<link rel="stylesheet" href="http://lycaon.asset/styles/app.css">`, "html"); err != nil {
		t.Fatalf("asset origin stylesheet rejected: %v", err)
	}
	for _, markup := range []string{
		`<img src="https://lycaon.asset/assets/logo.png">`,
		`<img src="https://cdn.example/logo.png">`,
		`<img src="http://lycaon.assetx/logo.png">`,
		`<link rel="stylesheet" href="http://lycaon.capture/app.css">`,
	} {
		if err := browser.ValidateMarkup(markup, "html"); err == nil {
			t.Fatalf("non-hermetic origin accepted: %s", markup)
		}
	}
}

func TestRenderAssetCatalogSummaryShape(t *testing.T) {
	t.Parallel()
	// kit.assets rides the existing free-form tool-result catalog — the field
	// existing with this shape is the wire contract (no OpenAPI typing).
	sum := designkit.AssetCatalogSummary{Count: 1, SamplePaths: []string{"assets/logo.png"}}
	cat := designkit.Catalog{Assets: &sum}
	if cat.Assets.Count != 1 || cat.Assets.SamplePaths[0] != "assets/logo.png" {
		t.Fatal("AssetCatalogSummary shape drifted")
	}
}

func TestRenderAssetFenceHasNoEcosystemDenylist(t *testing.T) {
	t.Parallel()
	// The deny surface is .git/ plus the path jail — not a growing name list.
	src, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "browser", "asset_fetch.go"))
	contractcheck.FailErr(t, "read asset_fetch.go", err)
	for _, forbidden := range []string{"node_modules", "vendor/", ".npm", ".cargo"} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("asset fence grew an ecosystem denylist entry %q — the jail plus .git/ deny is the whole floor", forbidden)
		}
	}
}
