package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScanPackToolMetaMatchesBundledRegistry(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg, err := scanregistry.New(t.Context(), scanregistry.Options{
		ModuleRoot: filepath.Join(root, "lycaon"),
	})
	contractcheck.FailErr(t, "scan registry", err)

	sqlDB := testdbfixture.Open(t, "store.db")

	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load scan tool schemas", err)
	toolReg, err := tools.NewCatalogRegistry(schemas)
	contractcheck.FailErr(t, "new catalog registry", err)
	coord := scantest.Coordinator(t, scan.NewSQLStore(sqlDB), nil)
	if err := scantoolapi.RegisterScanTools(toolReg, coord, reg, scancadence.New(scan.StoreFromCoordinator(coord), coord, reg, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil); err != nil {
		contractcheck.FailErr(t, "RegisterScanTools", err)
	}
	meta, ok := toolReg.Meta("scan_pack")
	if !ok {
		t.Fatal("scan_pack not registered")
	}
	if !strings.Contains(meta.Description, "code scanners") {
		t.Fatalf("description does not describe the configured scan surface: %s", meta.Description)
	}
	enum := schemaCategoryEnum(t, meta.ArgsSchema)
	for _, want := range []string{"all", "sca", "secret", "sast", "security"} {
		if !enum[want] {
			t.Fatalf("schema enum missing %q; got %v", want, enum)
		}
	}
}

func TestBoardOrientationInjectIncludesPackBoardLegend(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	injectPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "guidance", "board-orientation.md")
	data, err := os.ReadFile(injectPath)
	contractcheck.FailErr(t, "read board-orientation.md", err)
	if !strings.Contains(string(data), `include_scan_legend`) || !strings.Contains(string(data), `pack-board-legend.md`) {
		t.Fatal("board-orientation.md must gate pack-board-legend partial on include_scan_legend")
	}
}

func TestBoardOrientationRenderIncludesLegendProse(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	out, err := renderer.Render(context.Background(), "board-orientation", map[string]any{
		"pack_sentinel":       "<!-- pack-board:v1 -->",
		"include_scan_legend": true,
		"lines":               []map[string]any{{"text": "Scans: complete · clean"}},
	})
	contractcheck.FailErr(t, "render board-orientation", err)
	for _, want := range []string{
		"per_scan",
		"scan_compare",
		"scan_id",
		"scan_pack",
		"Pack board (host)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered inject missing %q:\n%s", want, out)
		}
	}
}

func schemaCategoryEnum(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("schema missing properties")
	}
	cat, _ := props["categories"].(map[string]any)
	items, _ := cat["items"].(map[string]any)
	rawEnum, _ := items["enum"].([]any)
	out := make(map[string]bool, len(rawEnum))
	for _, v := range rawEnum {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}
