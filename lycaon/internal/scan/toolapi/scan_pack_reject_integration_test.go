//go:build integration

package toolapi_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/scan/scantest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanPackRejectsLintWithStructuredCode(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	reg := &fanOutMockRegistry{scanners: []scanbase.CodeScanner{
		&scantest.MockScanner{IDVal: "lycaon-sca", CategoryList: []api.ScanCategory{api.ScanCategorySCA}},
	}}
	scanStore := scanbase.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, scanStore, nil)
	toolReg := tools.NewDefaultRegistry()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfig", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)
	testutil.FailErr(t, "RegisterScanTools", scantoolapi.RegisterScanTools(toolReg, coord, scantoolapi.ResultViews{Reads: scanStore.Reads, FullPasses: scanStore.FullPasses}, reg, scancadence.New(scanStore, coord, reg, nil, scancfg.DefaultGatesConfig(), nil), rejectFmt, nil, nil))

	_, err = toolReg.Run(context.Background(), "scan_pack", map[string]any{
		"categories": []any{string(api.ScanCategorySecret)},
	}, scanToolContext("", t.TempDir(), "coordinator"))
	if err == nil {
		t.Fatal("expected reject")
	}
	if !strings.Contains(err.Error(), "SCAN_PACK_NO_HOST_ENGINE") {
		t.Fatalf("err = %q", err.Error())
	}
}
