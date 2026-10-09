package contract

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Forces every probe down its failure path. This non-test file cannot use
// _test.go symbols.
var errFixtureProbeFailed = errors.New("preflight fixture: forced probe failure")

// preflightDetailFixtures collects the Detail keys real probes emit, keyed by
// notice code. Two engineered Envs cover codes whose Detail differs by condition:
// OS_BELOW_FLOOR reports want/got for an old OS and only a reason when unreadable.
func preflightDetailFixtures(t testingT) map[string]map[string]any {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home dir: %v", err)
	}
	configDir := filepath.Join(home, ".config", "lycaon-usernotice-context")
	t.Cleanup(func() { _ = os.RemoveAll(configDir) })

	base := func() preflight.Env {
		return preflight.Env{
			ConfigDir:     configDir,
			FreeBytes:     func(string) (uint64, error) { return 1 << 20, nil },
			ProviderCount: func() int { return 0 },
			ResolveGitEngine: func() error {
				return &gitengine.UnavailableError{Reason: "missing"}
			},
			ResolveScanner: func() (string, string, error) { return "", "checksum", errFixtureProbeFailed },
			CheckBrowser: func(context.Context) (preflight.BrowserReason, error) {
				return preflight.ReasonBrowserManagedCacheMissing, errFixtureProbeFailed
			},
		}
	}

	belowFloor := base()
	belowFloor.OSProductVer = func() (string, error) { return "10.15", nil }

	unreadable := base()
	unreadable.OSProductVer = func() (string, error) { return "", errFixtureProbeFailed }

	// A resolver failing with EMFILE, so the descriptor-limit condition is
	// produced by a probe run like every other fixture here rather than by
	// hand-writing the Detail map the probe would have emitted.
	fileLimit := base()
	fileLimit.ResolveScanner = func() (string, string, error) {
		return "", "not_found", &fs.PathError{Op: "open", Path: "manifest.yaml", Err: syscall.EMFILE}
	}

	liteDown := base()
	liteDown.LiteSlotUnavailable = func() (bool, map[string]string) {
		return true, map[string]string{"reason": "timeout", "provider_id": "ollama-1"}
	}

	out := map[string]map[string]any{}
	for _, env := range []preflight.Env{belowFloor, unreadable, fileLimit, liteDown} {
		results, _ := preflight.Default().Run(context.Background(), env)
		for _, res := range results {
			if res.Code == "" {
				continue
			}
			keys := out[res.Code]
			if keys == nil {
				keys = map[string]any{}
				out[res.Code] = keys
			}
			for k, v := range res.Detail {
				keys[k] = v
			}
		}
	}
	return out
}

// testingT is the slice of *testing.T these fixtures need, so the helper can be
// shared by tests without importing testing into non-test files.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// contextFromPromptErrorFixtures pins ContextFromPromptError keys for entries
// that declare context_schema.optional in host/user-notices.
func contextFromPromptErrorFixtures() map[string]map[string]any {
	return map[string]map[string]any{
		"provider_response_interrupted": usernotice.ContextFromPromptError(
			&failure.ProviderResponseInterruptedError{ProviderID: "fixture", Model: "model", Cause: errors.New("stream interrupted")},
		),
		"provider_not_configured": usernotice.ContextFromPromptError(
			&failure.ProviderNotConfiguredError{ProviderID: "fireworks"},
		),
		"provider_empty_completion": usernotice.ContextFromPromptError(
			&failure.ProviderEmptyCompletionError{ProviderID: "fireworks", Model: "llama-3.1-8b", Terminal: true, Reason: "length"},
		),
		"provider_context_too_small": usernotice.ContextFromPromptError(
			&failure.ProviderContextTooSmallError{ProviderID: "desktop", Model: "qwen3.5:27b", PromptTokens: 9000, MaxContext: 4096},
		),
		"provider_tool_calls_unsupported": usernotice.ContextFromPromptError(
			&failure.ProviderToolCallsUnsupportedError{ProviderID: "desktop", Model: "gemma-2b"},
		),
		"provider_tool_calls_in_prose": usernotice.ContextFromPromptError(
			&failure.ProviderToolCallsInProseError{ProviderID: "desktop", Model: "qwen2.5-7b"},
		),
		"provider_rate_limited": usernotice.ContextFromPromptError(
			&failure.ProviderRateLimitedError{ProviderID: "fireworks", Model: "gpt-oss-20b", Status: 429, Attempts: 5},
		),
		"provider_overloaded": usernotice.ContextFromPromptError(
			&failure.ProviderOverloadedError{ProviderID: "together-ai-1", Model: "openai/gpt-oss-120b", Status: 503, Attempts: 3},
		),
		"provider_server_error": usernotice.ContextFromPromptError(
			&failure.ProviderServerError{
				ProviderID: "together-ai-1", Model: "zai-org/GLM-5.3-Flash", Status: 500, Attempts: 4,
			},
		),
		"provider_request_rejected": usernotice.ContextFromPromptError(
			&providerretry.ProviderRequestRejectedError{ProviderID: "cloudflare-workers-ai-1", Model: "@cf/zai-org/glm-5.3-flash", Status: 403, Reason: providerretry.RejectionCloudflareWorkersPaidRequired},
		),
		"thinking_override_unavailable": usernotice.ContextFromPromptError(
			&llm.ThinkingOverrideError{ProviderID: "fixture", Model: "model", Reason: "effort is not supported"},
		),
		"provider_unreachable": usernotice.ContextFromPromptError(
			&failure.ProviderUnreachableError{
				ProviderID: "openai-1", Model: "gpt-5", Attempts: 2,
			},
		),
		"provider_silent": usernotice.ContextFromPromptError(
			&failure.ProviderSilentError{
				ProviderID: "together-ai-1", Model: "openai/gpt-oss-120b",
				Attempts: 2, Silence: 30 * time.Second,
			},
		),
		"model_refused": usernotice.ContextFromPromptError(
			&providerretry.ModelRefusedError{
				ProviderID: "together-ai-1", Model: "Qwen/Qwen3-Next-80B-A3B-Instruct",
				Status: 400, Code: "model_not_available",
			},
		),
		"workflow_not_runnable": usernotice.ContextFromPromptError(
			&workflow.NotRunnableError{RunID: "11111111-2222-4333-8444-555555555555", Reason: "paused", Status: wire.WorkflowRunStatusPaused},
		),
		"grounding_escalated": usernotice.ContextFromPromptError(guidance.ErrGroundingEscalated),
		"workflow_active":     usernotice.ContextFromPromptError(workflow.ErrActiveRunExists),
		"session_spend_ceiling_reached": usernotice.ContextFromPromptError(
			&spendguard.CeilingReached{CeilingUSD: 5, SpentUSD: 5.12, Coverage: wire.CostEstimateLowerBound, UnpricedTokens: 100, UnknownChargedCalls: 2},
		),
		"host_fault": usernotice.ContextFromPromptError(&promptloop.HostFaultError{
			Tool: "write", CallRan: true, Cause: errors.New("fixture settlement refused"),
		}),
		"turn_closeout_tool_call": usernotice.ContextFromPromptError(&promptloop.ProseTurnToolCallError{
			ProviderID: "fireworks", Model: "glm", Tools: []string{"submit_verdict"}, CloseoutReason: "`submit_verdict` was refused 6 times in a row (SUBMIT_VERDICT_INVALID)",
		}),
		"prompt_failed":                usernotice.ContextFromPromptError(errors.New("boom")),
		"provider_catalog_unavailable": {"provider_id": "fixture", "model": "model"},
		// HTTP worktree land/bind refusals pass these keys via writeErrorCtx Details.
		"worktree_land_blocked": {
			"reason":      "conflict",
			"base_branch": "main",
			"current":     "feature",
			"conflicts":   []string{"README.md"},
		},
	}
}
