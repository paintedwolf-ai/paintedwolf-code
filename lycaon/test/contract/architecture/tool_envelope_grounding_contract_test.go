package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
)

// toolEnvelopeHintCodes are hint_code values carried inside successful tool JSON payloads.
var toolEnvelopeHintCodes = []string{
	"SCAN_LIST_EMPTY",
	"REPO_MAP_EMPTY",
	"TOOL_SURVEY_BYTE_CLAMPED",
}

func loadToolEnvelopeHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	return cfg
}

func TestToolEnvelopeHintCodesRegisteredAsBanner(t *testing.T) {
	t.Parallel()
	cfg := loadToolEnvelopeHintConfig(t)
	for _, code := range toolEnvelopeHintCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("hint registry missing envelope hint %q", code)
		}
		if entry.Emit != "banner" {
			t.Fatalf("hint %q emit=%q want banner", code, entry.Emit)
		}
		view := guidance.NormalizeRejectCodeView(code, entry)
		for _, field := range []struct{ name, val string }{
			{"message", entry.Message},
			{"what", view.What},
			{"fix", view.Fix},
			{"instead", view.Instead},
		} {
			if strings.TrimSpace(field.val) == "" {
				t.Fatalf("hint %q missing %s", code, field.name)
			}
		}
	}
}

func TestToolEnvelopeHintCodesHaveProductionEmission(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	scanResult, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission", err)
	for _, code := range toolEnvelopeHintCodes {
		if !scanResult.HasEmissionSite(code) {
			t.Fatalf("tool envelope hint %q has no production emission site", code)
		}
	}
}

func TestToolEnvelopePackagesNoInlineHintProseConsts(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	sites, err := guidancescan.ScanInlineEnvelopeHintConsts(lycaonRoot)
	contractcheck.FailErr(t, "scan inline envelope hint consts", err)
	if len(sites) > 0 {
		t.Fatalf("tool envelope packages must not define inline Hint prose consts (use hint registry + BundledEnvelopeHint):\n%s",
			strings.Join(sites, "\n"))
	}
}

func TestSurveyByteClampUsesRegisteredHintCode(t *testing.T) {
	t.Parallel()
	cfg := loadToolEnvelopeHintConfig(t)
	clamped := `{"results":[],"offset":0,"truncated":true,"next_offset":0,"truncation_banner":"trimmed"}`
	vars := map[string]any{"kept": 0, "page_key": "results", "offset": 0, "next_offset": 0}
	msg := guidance.EnvelopeHintMessage(t.Context(), cfg, nil, "TOOL_SURVEY_BYTE_CLAMPED", vars)
	out := guidance.AppendOutputBanner(clamped, "TOOL_SURVEY_BYTE_CLAMPED", msg)
	if !strings.Contains(out, "Code: TOOL_SURVEY_BYTE_CLAMPED") {
		t.Fatalf("missing registered banner code: %q", out)
	}
	if strings.Contains(msg, "{{") {
		t.Fatalf("hint message must render template vars: %q", msg)
	}
	if !strings.Contains(out, clamped) {
		t.Fatal("banner must preserve clamped JSON prefix")
	}
}
