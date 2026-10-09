package usernotice

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestContextFromPromptErrorProvider(t *testing.T) {
	ctx := ContextFromPromptError(&failure.ProviderNotConfiguredError{ProviderID: "openai"})
	if ctx["provider_id"] != "openai" {
		t.Fatalf("ctx = %#v", ctx)
	}
}

func TestContextFromPromptErrorEmptyCompletion(t *testing.T) {
	ctx := ContextFromPromptError(&failure.ProviderEmptyCompletionError{
		ProviderID: "fireworks",
		Model:      "llama-3.1-8b",
	})
	if ctx["provider_id"] != "fireworks" || ctx["model"] != "llama-3.1-8b" {
		t.Fatalf("ctx = %#v", ctx)
	}
}

func TestOutputLimitNoticeUsesTerminalReason(t *testing.T) {
	catalog := loadTestCatalog(t)
	for _, reason := range []string{"length", "max_tokens", "MAX_TOKENS", "provider said length in diagnostic prose"} {
		ctx := ContextFromPromptError(&failure.ProviderEmptyCompletionError{Reason: reason, Terminal: true})
		copy, ok := catalog.Render("provider_empty_completion", ctx)
		if !ok {
			t.Fatal("empty completion notice did not render")
		}
		if got := strings.Contains(copy.Message, "output budget"); got != (reason != "provider said length in diagnostic prose") {
			t.Fatalf("output limit notice = %+v for reason %q", copy, reason)
		}
	}
}

func TestContextFromInterruptedResponseUsesProviderIdentity(t *testing.T) {
	ctx := ContextFromPromptError(&failure.ProviderResponseInterruptedError{
		ProviderID: "fixture", Model: "model", Cause: errors.New("private transport diagnostic"),
	})
	if len(ctx) != 2 || ctx["provider_id"] != "fixture" || ctx["model"] != "model" {
		t.Fatalf("interrupted response context = %#v", ctx)
	}
}

func TestContextFromPromptErrorProviderServerUsesOnlyStructuredFacts(t *testing.T) {
	ctx := ContextFromPromptError(&failure.ProviderServerError{
		ProviderID: "together-ai-1",
		Model:      "zai-org/GLM-5.3-Flash",
		Status:     500,
		Attempts:   4,
		Detail:     "EngineCore encountered an issue. See stack trace above.",
	})
	if ctx["provider_id"] != "together-ai-1" || ctx["model"] != "zai-org/GLM-5.3-Flash" ||
		ctx["status"] != 500 || ctx["attempts"] != 4 {
		t.Fatalf("server error ctx = %#v", ctx)
	}
	if _, leaked := ctx["detail"]; leaked {
		t.Fatalf("server error context leaked provider prose: %#v", ctx)
	}
}

func TestCloudflarePaidPlanNoticeUsesStructuredReason(t *testing.T) {
	catalog := loadTestCatalog(t)
	for _, reason := range []providerretry.ProviderRejectionReason{"", providerretry.RejectionCloudflareWorkersPaidRequired} {
		err := &providerretry.ProviderRequestRejectedError{
			ProviderID: "cloudflare-workers-ai-1", Model: "@cf/zai-org/glm-5.3-flash", Status: 403,
			Reason: reason, Cause: errors.New("private provider diagnostic: Workers Paid plan 5035"),
		}
		ctx := ContextFromPromptError(err)
		copy, ok := catalog.Render("provider_request_rejected", ctx)
		if !ok {
			t.Fatal("provider rejection notice did not render")
		}
		if strings.Contains(copy.Message, "private provider diagnostic") {
			t.Fatalf("provider diagnostics leaked into notice: %s", copy.Message)
		}
		if reason == "" {
			if strings.Contains(copy.Message, "Workers Paid plan") {
				t.Fatalf("provider prose was treated as a plan restriction: %s", copy.Message)
			}
			continue
		}
		if !strings.Contains(copy.Message, err.Model) || !strings.Contains(copy.Message, "Workers Paid plan") ||
			!strings.Contains(copy.SuggestedAction, "Workers Free plan") {
			t.Fatalf("paid plan notice lacks actionable guidance: %+v", copy)
		}
	}
}

func TestContextFromPromptErrorNotRunnable(t *testing.T) {
	ctx := ContextFromPromptError(&runstate.NotRunnableError{Reason: "paused", Status: wire.WorkflowRunStatusPaused})
	if ctx["reason"] != "paused" {
		t.Fatalf("ctx = %#v", ctx)
	}
}

func TestContextFromPromptErrorGeneric(t *testing.T) {
	ctx := ContextFromPromptError(errors.New("boom"))
	if ctx["detail"] != "boom" {
		t.Fatalf("generic error ctx = %#v want detail=boom", ctx)
	}
	if ctx := ContextFromPromptError(session.ErrGroundingEscalated); len(ctx) != 0 {
		t.Fatalf("grounding ctx = %#v want empty map", ctx)
	}
}

func TestContextFromSpendCeilingErrorDisclosesPartialPricing(t *testing.T) {
	ctx := ContextFromPromptError(&session.SessionSpendCeilingReached{
		CeilingUSD: 5, SpentUSD: 5.12, Coverage: wire.CostEstimateLowerBound, UnpricedTokens: 100, UnknownChargedCalls: 2,
	})
	if ctx["ceiling_usd"] != "5.00" || ctx["spent_usd"] != "5.12" || ctx["estimate_coverage"] != "lower_bound" || ctx["unpriced_tokens"] != 100 || ctx["unknown_charged_calls"] != 2 {
		t.Fatalf("spend ceiling ctx = %#v", ctx)
	}
}

func TestSanitizePromptFailureDetail(t *testing.T) {
	got := sanitizePromptFailureDetail("openai: HTTP 401: Authorization: Bearer secret-token-value")
	if !strings.Contains(got, "openai: HTTP 401") {
		t.Fatalf("detail missing cause: %q", got)
	}
	if strings.Contains(got, "secret-token-value") {
		t.Fatalf("detail leaked bearer token: %q", got)
	}
	long := strings.Repeat("x", maxPromptFailureDetailRunes+40)
	truncated := sanitizePromptFailureDetail(long)
	if utf8.RuneCountInString(truncated) != maxPromptFailureDetailRunes {
		t.Fatalf("truncated len = %d want %d (%q)", utf8.RuneCountInString(truncated), maxPromptFailureDetailRunes, truncated)
	}
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("truncated missing ellipsis: %q", truncated)
	}
}

func TestRenderWireUsesContextFromPromptError(t *testing.T) {
	cfg, err := LoadNoticeDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load host/user-notices", err)
	catalog := NewCatalog(cfg)
	copy := catalog.RenderWire("provider_not_configured", ContextFromPromptError(
		&failure.ProviderNotConfiguredError{ProviderID: "fireworks"},
	))
	if copy.Message == "" || copy.Title == "" {
		t.Fatalf("copy = %#v", copy)
	}
}
