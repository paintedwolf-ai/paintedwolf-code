package integration

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFormatPackReject(t *testing.T) {
	cfg := moduleHintConfig(t)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	rejectFmt := guidance.NewStaticRejectFormatter(cfg)

	err := scan.FormatPackReject(&scan.PackReject{
		Code: "SCAN_PACK_NO_HOST_ENGINE",
		Data: map[string]any{
			"categories":       "lint",
			"project_dir":      "/tmp/p",
			"engines":          "lycaon-sca",
			"valid_categories": "sca, secret, all",
		},
	}, rejectFmt)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "SCAN_PACK_NO_HOST_ENGINE") || !strings.Contains(err.Error(), "Rejected:") {
		t.Fatalf("err = %q", err.Error())
	}
}

func TestFormatPackRejectPassesThrough(t *testing.T) {
	plain := errors.New("plain")
	got := scan.FormatPackReject(plain, guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	if !errors.Is(got, plain) {
		t.Fatal("expected same error")
	}
}

func moduleHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	return cfg
}

func TestScanRefusalsRetainCodeAndDetailsWithoutFormatter(t *testing.T) {
	for _, err := range []error{
		scan.FormatPackReject(&scan.PackReject{Code: "SCAN_PACK_NO_HOST_ENGINE", Data: map[string]any{"count": 7}}, nil),
		scan.FormatDrilldownReject(&scan.DrilldownReject{Code: "SCAN_NOT_FOUND", Data: map[string]any{"count": 7}}, nil),
		scantoolapi.FormatCompareReject(&scan.CompareReject{Code: "SCAN_COMPARE_TOO_LARGE", Data: map[string]any{"count": 7}}, nil),
	} {
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code == "" || reject.Data["count"] != 7 {
			t.Fatalf("lost structured scan refusal: %#v / %v", reject, err)
		}
	}
}
