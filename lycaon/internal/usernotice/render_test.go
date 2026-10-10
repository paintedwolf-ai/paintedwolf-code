package usernotice

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func loadTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load host/user-notices", err)
	return NewCatalog(cfg)
}

func TestRenderNoticeCopyDefaults(t *testing.T) {
	copy := RenderNoticeCopy(NoticeCopy{Title: "Title", Message: "Message"}, nil)
	if copy.Title != "Title" || copy.Message != "Message" {
		t.Fatalf("copy = %#v", copy)
	}
}

func TestRenderProviderNotConfiguredWithContext(t *testing.T) {
	catalog := loadTestCatalog(t)
	copy, ok := catalog.Render("provider_not_configured", map[string]any{"provider_id": "fireworks"})
	if !ok {
		t.Fatal("expected render ok")
	}
	if !strings.Contains(copy.Message, "fireworks") {
		t.Fatalf("message = %q", copy.Message)
	}
	if strings.Contains(copy.Message, "{{") {
		t.Fatalf("unrendered template in message: %q", copy.Message)
	}
}

func TestRenderProviderNotConfiguredFallback(t *testing.T) {
	catalog := loadTestCatalog(t)
	copy, ok := catalog.Render("provider_not_configured", map[string]any{})
	if !ok {
		t.Fatal("expected render ok")
	}
	if !strings.Contains(copy.Message, "language model") {
		t.Fatalf("message = %q", copy.Message)
	}
}

func TestRenderFailureNeverReturnsAuthoredTemplateSource(t *testing.T) {
	raw := "{{ invalid"
	copy := RenderNoticeCopy(NoticeCopy{Message: raw}, nil)
	if copy.Message == raw || strings.Contains(copy.Message, "{{") {
		t.Fatalf("render failure exposed template source: %q", copy.Message)
	}
}

func TestValidationDoesNotSkipDotPrefixedTemplateSyntax(t *testing.T) {
	cfg := &Config{
		Defaults: NoticeCopy{Title: "Title", Message: "Message", SuggestedAction: "Act"},
		UserNotices: map[string]Entry{
			"bad_template": {
				Surfaces: []string{"http"}, Title: "Title", Message: "{{.value}}", SuggestedAction: "Act",
			},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("dot-prefixed invalid template bypassed validation")
	}
}

type hostFaultFixture struct {
	tool    string
	callRan bool
}

func (hostFaultFixture) Error() string { return "fixture host fault" }

func (hostFaultFixture) NoticeCode() wire.NoticeCode { return wire.NoticeCodeHostFault }

func (f hostFaultFixture) NoticeHostFault() (string, bool) { return f.tool, f.callRan }

// The host-fault notice names the call and says its changes may stand only
// when the call ran; it never offers the failure detail or a plain resend.
func TestHostFaultNoticeStatesWhetherTheCallRan(t *testing.T) {
	catalog := loadTestCatalog(t)
	for _, callRan := range []bool{false, true} {
		ctx := ContextFromPromptError(hostFaultFixture{tool: "write", callRan: callRan})
		ctx[ContextTurnProgress] = TurnProgressMade
		copy, ok := catalog.Render(string(wire.NoticeCodeHostFault), ctx)
		if !ok {
			t.Fatal("host-fault notice did not render")
		}
		if !strings.Contains(copy.Message, "a write call") ||
			strings.Contains(copy.Message, "may be in place") != callRan ||
			strings.Contains(copy.Message, "fixture host fault") {
			t.Fatalf("host-fault message (call ran=%v): %s", callRan, copy.Message)
		}
		if !strings.Contains(copy.SuggestedAction, "will likely stop the same way") {
			t.Fatalf("host-fault action: %s", copy.SuggestedAction)
		}
	}
}

func TestSpendCeilingNoticeCoverage(t *testing.T) {
	catalog := loadTestCatalog(t)
	for _, tc := range []struct {
		name              string
		unpriced, unknown int
	}{
		{"complete", 0, 0}, {"unpriced", 100, 0}, {"unreported", 0, 1}, {"both", 100, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coverage := wire.CostEstimateComplete
			if tc.unpriced > 0 || tc.unknown > 0 {
				coverage = wire.CostEstimateLowerBound
			}
			ctx := ContextFromPromptError(&spendguard.CeilingReached{CeilingUSD: 5, SpentUSD: 5.12, Coverage: coverage, UnpricedTokens: tc.unpriced, UnknownChargedCalls: tc.unknown})
			copy, ok := catalog.Render("session_spend_ceiling_reached", ctx)
			if !ok {
				t.Fatal("spend notice did not render")
			}
			if strings.Contains(copy.Message, "at least") != (tc.unpriced > 0 || tc.unknown > 0) || strings.Contains(copy.Message, "no price") != (tc.unpriced > 0) || strings.Contains(copy.Message, "unreported") != (tc.unknown > 0) {
				t.Fatalf("incorrect coverage: %s", copy.Message)
			}
		})
	}
}
