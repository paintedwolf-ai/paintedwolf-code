package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
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
