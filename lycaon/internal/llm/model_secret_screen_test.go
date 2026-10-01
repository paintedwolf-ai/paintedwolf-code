package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const modelScreenGitHubToken = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
const modelScreenJWT = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"

func modelScreenMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("m", 32)))
	testutil.FailErr(t, "build fingerprinter", err)
	m.SetFingerprinter(fingerprinter)
	return m
}

func modelSecretRequest() modelcall.CompletionRequest {
	return modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "1|public\n2|token=" + modelScreenGitHubToken,
			ToolResult: &api.ToolResult{
				Tool:       "read",
				ToolCallID: "call_read_1",
				ToolArgs:   map[string]any{"path": ".env.local"},
			},
		}},
		Tools: []tools.ToolMeta{{
			Name: "write", Description: "Write a file", ArgsSchema: map[string]any{"type": "object"},
		}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1", ProjectID: "proj-1"},
	}
}

func TestModelSecretScreenRedactsEphemeralRequestWithProvenance(t *testing.T) {
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	original := modelSecretRequest()
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "fireworks-main", Label: "Fireworks"}, original)
	testutil.FailErr(t, "screen redacted request", err)
	if strings.Contains(got.Messages[0].Content, modelScreenGitHubToken) {
		t.Fatal("redacted request retained token")
	}
	if !strings.Contains(got.Messages[0].Content, "[REDACTED]") {
		t.Fatalf("redacted content = %q", got.Messages[0].Content)
	}
	if got.Messages[0].HostSecretRedaction == nil || got.Messages[0].HostSecretRedaction.Occurrences() != 1 {
		t.Fatalf("host redaction provenance = %+v", got.Messages[0].HostSecretRedaction)
	}
	if len(got.Messages) != 2 || got.Messages[1].Role != api.MessageRoleSystem ||
		got.Messages[1].Content != HostSecretRedactionNotice(true, false) {
		t.Fatalf("host redaction notice = %+v", got.Messages)
	}
	if !strings.Contains(original.Messages[0].Content, modelScreenGitHubToken) {
		t.Fatal("screen mutated durable/original request")
	}
	if finding.RuleID != "gitleaks:github-pat" || finding.RuleTitle == "" {
		t.Fatalf("matcher shape = %+v", finding)
	}
	if finding.SourceKind != secretmatch.SourceToolResult || finding.SourceTool != "read" || finding.SourcePath != ".env.local" || finding.SourceLine != 2 {
		t.Fatalf("provenance = %+v", finding)
	}
	if finding.OriginKind != secretmatch.OriginFile || finding.SourceToolCallID != "call_read_1" {
		t.Fatalf("origin = kind=%q call=%q", finding.OriginKind, finding.SourceToolCallID)
	}
	if finding.Surface != secretmatch.SurfaceModel || !finding.Surface.CanRedact() || finding.DestinationID != "fireworks-main" || finding.DestinationLabel != "Fireworks" || finding.Occurrences != 1 {
		t.Fatalf("destination/count = %+v", finding)
	}
	if len(finding.Fingerprints) != 1 {
		t.Fatalf("fingerprints = %v, want one exact secret identity", finding.Fingerprints)
	}
}

func TestModelSecretScreenTracksDetectedValueAndSendsOnlyReference(t *testing.T) {
	m := modelScreenMatcher(t)
	harvest := secretharvest.NewRuntime(nil)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		values := harvest.ValuesFor("sess-1")
		out := make([]secretmatch.HarvestedValue, 0, len(values))
		for _, value := range values {
			out = append(out, secretmatch.HarvestedValue{
				Name: value.Name, Container: value.Container, Secret: value.Secret(),
				RuleID: value.RuleID, Title: value.Title, Source: value.Source,
				Reference: value.Reference, NonDisclosable: value.NonDisclosable,
			})
		}
		return out
	})
	m.SetRemember(func(root string, values []secretmatch.Remembered) { harvest.Remember(root, values...) })
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.TrackAndReplace}, nil
	})
	const reference = "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}"
	var adopted ManagedSecretAdoptRequest
	screen.SetManagedSecretAdopter(func(_ context.Context, req ManagedSecretAdoptRequest) (string, error) {
		adopted = req
		m.Remember(req.RootSessionID, []secretmatch.Remembered{{
			Secret: req.Value, Name: req.Name, Origin: "managed secret", RuleID: "managed-secret",
			Title: "A managed secret", Source: secretmatch.SourceRememberedMatch,
			Reference: reference, NonDisclosable: true,
		}})
		return reference, nil
	})
	req := modelSecretRequest()
	req.Debug.RootSessionID = "sess-1"
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "track detected value", err)
	if adopted.Value != modelScreenGitHubToken || adopted.ProjectID != "proj-1" || adopted.RootSessionID != "sess-1" {
		t.Fatalf("adoption = %+v", adopted)
	}
	if strings.Contains(got.Messages[0].Content, modelScreenGitHubToken) ||
		strings.Contains(got.Messages[0].Content, "[REDACTED]") ||
		!strings.Contains(got.Messages[0].Content, reference) {
		t.Fatalf("provider-bound content = %q", got.Messages[0].Content)
	}
	if !strings.Contains(req.Messages[0].Content, modelScreenGitHubToken) {
		t.Fatal("tracking mutated the source request")
	}
}

func TestModelSecretScreenSubstitutesManagedSecretReference(t *testing.T) {
	m := modelScreenMatcher(t)
	harvest := secretharvest.NewRuntime(nil)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		values := harvest.ValuesFor("root-1")
		out := make([]secretmatch.HarvestedValue, 0, len(values))
		for _, value := range values {
			out = append(out, secretmatch.HarvestedValue{
				Name: value.Name, Container: value.Container, Secret: value.Secret(),
				RuleID: value.RuleID, Title: value.Title, Source: value.Source,
				Reference: value.Reference, NonDisclosable: value.NonDisclosable,
			})
		}
		return out
	})
	m.SetRemember(func(root string, values []secretmatch.Remembered) {
		harvest.Remember(root, values...)
	})
	const generated = "host-generated-value-1234567890"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	m.Remember("root-1", []secretmatch.Remembered{{
		Secret: generated, Name: "deployment token", Origin: "managed secret",
		RuleID: "managed-secret", Title: "A managed secret",
		Source: secretmatch.SourceRememberedMatch, Reference: reference, NonDisclosable: true,
	}})

	asks := 0
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		asks++
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	original := modelcall.CompletionRequest{Messages: []api.Message{{
		Role: api.MessageRoleTool, Content: strings.Repeat("prefix"+generated+"suffix ", 20),
		ToolResult: &api.ToolResult{Tool: "http_request", Outcome: api.ToolResultOutcomeCompleted},
	}}, Debug: modelcall.RequestDebug{SessionID: "root-1", RootSessionID: "root-1"}}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, original)
	testutil.FailErr(t, "screen generated secret", err)
	if asks != 0 {
		t.Fatalf("provider disclosure was offered for host-generated material: asks=%d", asks)
	}
	if strings.Contains(got.Messages[0].Content, generated) ||
		strings.Contains(got.Messages[0].Content, "[REDACTED]") ||
		!strings.Contains(got.Messages[0].Content, reference) {
		t.Fatalf("provider-bound content = %q", got.Messages[0].Content)
	}
	if !strings.Contains(original.Messages[0].Content, generated) {
		t.Fatal("provider screen mutated the durable tool result")
	}
}

func TestModelSecretScreenPrimesPersistentManagedSecretBeforeInspecting(t *testing.T) {
	m := modelScreenMatcher(t)
	harvest := secretharvest.NewRuntime(nil)
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		values := harvest.ValuesFor("root-2")
		out := make([]secretmatch.HarvestedValue, 0, len(values))
		for _, value := range values {
			out = append(out, secretmatch.HarvestedValue{
				Name: value.Name, Container: value.Container, Secret: value.Secret(),
				RuleID: value.RuleID, Title: value.Title, Source: value.Source,
				Reference: value.Reference, NonDisclosable: value.NonDisclosable,
			})
		}
		return out
	})
	m.SetRemember(func(root string, values []secretmatch.Remembered) {
		harvest.Remember(root, values...)
	})
	const generated = "managed-generated-value-with-no-shape"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a151}}"
	asks := 0
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		asks++
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	screen.SetManagedSecretEvidence(func(_ context.Context, projectID, rootSessionID string) ([]secretmatch.Remembered, error) {
		if projectID != "proj-1" || rootSessionID != "root-2" {
			t.Fatalf("primer attribution = %q/%q", projectID, rootSessionID)
		}
		return []secretmatch.Remembered{{
			Secret: generated, Name: "deployment token", Origin: "managed secret",
			RuleID: "managed-secret", Title: "A managed secret",
			Source: secretmatch.SourceRememberedMatch, Reference: reference, NonDisclosable: true,
		}}, nil
	})
	req := modelcall.CompletionRequest{Messages: []api.Message{{
		Role: api.MessageRoleTool, Content: "prefix" + generated + "suffix",
	}}, Debug: modelcall.RequestDebug{SessionID: "root-2", RootSessionID: "root-2", ProjectID: "proj-1"}}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen primed secret", err)
	if asks != 0 || strings.Contains(got.Messages[0].Content, generated) ||
		strings.Contains(got.Messages[0].Content, "[REDACTED]") ||
		!strings.Contains(got.Messages[0].Content, reference) {
		t.Fatalf("provider request asks=%d content=%q", asks, got.Messages[0].Content)
	}
}

func TestModelSecretScreenFailsClosedWhenManagedSecretPrimerFails(t *testing.T) {
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	boom := errors.New("protected value store unavailable")
	screen.SetManagedSecretEvidence(func(context.Context, string, string) ([]secretmatch.Remembered, error) { return nil, boom })
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "public"}},
		Debug:    modelcall.RequestDebug{SessionID: "root-1", ProjectID: "proj-1"},
	})
	var fault *ModelRequestSecretScreenFaultError
	if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageManagedSecretStore || !errors.Is(err, boom) {
		t.Fatalf("primer error = %v", err)
	}
}

func TestModelSecretScreenSendUnchangedAndDeny(t *testing.T) {
	m := modelScreenMatcher(t)
	req := modelSecretRequest()
	allow := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	got, err := allow.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	if err != nil || got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("unchanged: content=%q err=%v", got.Messages[0].Content, err)
	}

	// An unanswered card fails closed.
	unanswered := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Unanswered}, nil
	})
	if _, err := unanswered.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req); !errors.Is(err, ErrModelRequestSecretUnanswered) {
		t.Fatalf("unanswered error = %v", err)
	}
}

// Ask failures remain distinct from human decisions.
func TestModelSecretScreenAskFaultIsNotADecision(t *testing.T) {
	m := modelScreenMatcher(t)
	req := modelSecretRequest()
	boom := errors.New("secret approval plan target has no generic shape")
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{}, secretmatch.NewAskFault(secretmatch.FaultStageRaise, boom)
	})
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	if !errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatalf("fault error = %v, want a screen-failed fault", err)
	}
	if errors.Is(err, ErrModelRequestSecretWithheld) || errors.Is(err, ErrModelRequestSecretUnanswered) {
		t.Fatalf("fault reported as a human decision: %v", err)
	}
	var fault *ModelRequestSecretScreenFaultError
	if !errors.As(err, &fault) {
		t.Fatalf("err = %T, want *ModelRequestSecretScreenFaultError", err)
	}
	if fault.Stage != secretmatch.FaultStageRaise {
		t.Fatalf("stage = %q, want %q", fault.Stage, secretmatch.FaultStageRaise)
	}
	// The fault retains its root cause.
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying raise failure", err)
	}
}

// Missing ask wiring returns a fault.
func TestModelSecretScreenWithoutAskFaults(t *testing.T) {
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, modelSecretRequest())
	var fault *ModelRequestSecretScreenFaultError
	if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageScreenUnwired {
		t.Fatalf("err = %v, want a screen_unwired fault", err)
	}
}

func TestModelSecretScreenRelaysCompletedPublicFetchWithoutApproval(t *testing.T) {
	exec := tools.NewDefaultToolExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := NewModelSecretScreen(modelScreenMatcher(t), exec.AskSecretScreen)
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "docs example\nPASSWORD=" + modelScreenGitHubToken,
			ToolResult: &api.ToolResult{
				Tool: "fetch_url", Outcome: api.ToolResultOutcomeCompleted,
				ToolArgs: map[string]any{"url": "https://docs.example/install"},
			},
		}},
	}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "relay public fetch result", err)
	if got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("public result changed: %q", got.Messages[0].Content)
	}
}

// Trust is a fact the screen carries; the gate decides what it means.
func TestModelSecretScreenCarriesDestinationTrustOnTheAlert(t *testing.T) {
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	trusted := ScreenDestination{ID: "ollama-1@abcd", Label: "Ollama", Trusted: true}
	_, err := screen.Screen(context.Background(), trusted, modelSecretRequest())
	testutil.FailErr(t, "screen trusted destination", err)
	if !finding.DestinationTrusted || finding.DestinationID != trusted.ID || finding.DestinationLabel != "Ollama" {
		t.Fatalf("alert destination = %+v, want the trusted destination as resolved", finding)
	}
	_, err = screen.Screen(context.Background(), ScreenDestination{ID: "ollama-1@abcd", Label: "Ollama"}, modelSecretRequest())
	testutil.FailErr(t, "screen untrusted destination", err)
	if finding.DestinationTrusted {
		t.Fatal("an untrusted destination reported trust")
	}
}

// Trusted destinations bypass the approval card.
func TestModelSecretScreenTrustedDestinationSendsUnchangedWithoutACard(t *testing.T) {
	exec := tools.NewDefaultToolExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := NewModelSecretScreen(modelScreenMatcher(t), exec.AskSecretScreen)
	req := modelSecretRequest()

	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "ollama-1", Trusted: true}, req)
	testutil.FailErr(t, "screen trusted destination", err)
	if got.Messages[0].Content != req.Messages[0].Content {
		t.Fatalf("trusted send changed the request: %q", got.Messages[0].Content)
	}

	_, err = screen.Screen(context.Background(), ScreenDestination{ID: "ollama-1"}, req)
	var fault *ModelRequestSecretScreenFaultError
	if !errors.As(err, &fault) || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("untrusted destination err = %v, want the card path to be reached", err)
	}
}

// Trust does not disclose protected managed values.
func TestModelSecretScreenTrustedDestinationStillWithholdsNonDisclosableValues(t *testing.T) {
	m := modelScreenMatcher(t)
	const generated = "host-generated-value-1234567890"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name: "deployment token", Secret: generated, RuleID: secretmatch.ManagedRuleID,
			Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
			Reference: reference, NonDisclosable: true,
		}}
	})
	exec := tools.NewDefaultToolExecutor(nil, tools.NewDefaultRegistry(), "implement")
	screen := NewModelSecretScreen(m, exec.AskSecretScreen)
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleTool,
			Content: "token=" + generated + "\nalso=" + modelScreenGitHubToken,
			ToolResult: &api.ToolResult{
				Tool: "read", ToolCallID: "call_read_1", ToolArgs: map[string]any{"path": ".env"},
			},
		}},
		Debug: modelcall.RequestDebug{SessionID: "root-1", RootSessionID: "root-1"},
	}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "ollama-1", Trusted: true}, req)
	testutil.FailErr(t, "screen trusted destination with a managed value", err)
	content := got.Messages[0].Content
	if strings.Contains(content, generated) || !strings.Contains(content, reference) {
		t.Fatalf("managed value crossed to a trusted provider: %q", content)
	}
	if !strings.Contains(content, modelScreenGitHubToken) {
		t.Fatalf("trusted send stripped a detected value it should have sent unchanged: %q", content)
	}
}

func TestModelSecretScreenDoesNotTrustFailedFetchResult(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: []api.Message{{
		Role:       api.MessageRoleTool,
		Content:    "PASSWORD=" + modelScreenGitHubToken,
		ToolResult: &api.ToolResult{Tool: "fetch_url", Outcome: api.ToolResultOutcomeError},
	}}}
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen failed fetch-shaped result", err)
	if finding.Source != "unknown" {
		t.Fatalf("source = %q want unknown", finding.Source)
	}
}

func TestModelSecretScreenPublicHitCannotMaskUnknownHit(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: []api.Message{
		{
			Role: api.MessageRoleTool, Content: "public token=" + modelScreenGitHubToken,
			ToolResult: &api.ToolResult{Tool: "fetch_url", Outcome: api.ToolResultOutcomeCompleted},
		},
		{
			Role: api.MessageRoleTool, Content: "local token=" + modelScreenGitHubToken,
			ToolResult: &api.ToolResult{Tool: "read", Outcome: api.ToolResultOutcomeCompleted},
		},
	}}
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen mixed public and unknown sources", err)
	if finding.Source != "unknown" || finding.SourceTool != "read" {
		t.Fatalf("primary source = %q/%q want unknown/read", finding.Source, finding.SourceTool)
	}
}

func TestModelSecretScreenWithholdSendsNothingAndCarriesGuidance(t *testing.T) {
	m := modelScreenMatcher(t)
	req := modelSecretRequest()
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		return secretmatch.Resolution{Decision: secretmatch.Withhold, Guidance: "use the staging key"}, nil
	})
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	if !errors.Is(err, ErrModelRequestSecretWithheld) {
		t.Fatalf("withhold error = %v", err)
	}
	// A refusal returns no provider-bound request.
	if len(got.Messages) != 0 {
		t.Fatalf("withheld screen returned a sendable request: %+v", got)
	}
	if errors.Is(err, ErrModelRequestSecretUnanswered) || errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatal("an answered refusal must not read as an unanswered card or a host fault")
	}
	if guidance := SecretWithheldGuidance(err); guidance != "use the staging key" {
		t.Fatalf("guidance = %q", guidance)
	}
	if got := SecretWithheldGuidance(ErrModelRequestSecretUnanswered); got != "" {
		t.Fatalf("guidance from unrelated error = %q", got)
	}
}

func TestModelSecretScreenScansToolArgumentsAndDefinitions(t *testing.T) {
	m := modelScreenMatcher(t)
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
			Name: "command", Args: map[string]any{"env": []any{"TOKEN=" + modelScreenGitHubToken}},
		}}}},
		Tools: []tools.ToolMeta{{Name: "fixture", ArgsSchema: map[string]any{
			"description": "example " + modelScreenGitHubToken,
		}}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1"},
	}
	var occurrences int
	screen := NewModelSecretScreen(m, func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		occurrences = finding.Occurrences
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen nested request", err)
	if occurrences != 2 {
		t.Fatalf("occurrences = %d want 2", occurrences)
	}
	if strings.Contains(strings.Join([]string{
		got.Messages[0].ToolCalls[0].Args["env"].([]any)[0].(string),
		got.Tools[0].ArgsSchema["description"].(string),
	}, " "), modelScreenGitHubToken) {
		t.Fatal("nested request fields retained token")
	}
}

// Replayed reasoning passes through the outbound screen.
func TestModelSecretScreenAsksAboutASecretOnlyInReplayedReasoning(t *testing.T) {
	m := modelScreenMatcher(t)
	asks := 0
	screen := NewModelSecretScreen(m, func(_ context.Context, finding secretmatch.Alert) (secretmatch.Resolution, error) {
		asks++
		if finding.RuleID == "" {
			t.Fatalf("card carried no rule identity: %+v", finding)
		}
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:    api.MessageRoleAssistant,
			Content: "I will retry the deploy.",
			ModelReasoning: &api.ModelReasoning{
				Text:    "The relay rejected me; the env file had TOKEN=" + modelScreenGitHubToken,
				Details: []json.RawMessage{json.RawMessage(`"retry with the same credential"`)},
			},
		}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1"},
	}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen replayed reasoning", err)
	if asks != 1 {
		t.Fatalf("asks = %d, want one card for a credential bound for the provider", asks)
	}
	// A matching secret removes the complete signed trace.
	if got.Messages[0].ModelReasoning != nil {
		t.Fatalf("reasoning survived redaction: %+v", got.Messages[0].ModelReasoning)
	}
	if req.Messages[0].ModelReasoning == nil {
		t.Fatal("screen mutated the source request")
	}
}

// Protected managed values are withheld from every request field.
func TestModelSecretScreenWithholdsANonDisclosableValueFromReasoning(t *testing.T) {
	m := modelScreenMatcher(t)
	const generated = "host-generated-value-1234567890"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Name: "deployment token", Secret: generated, RuleID: secretmatch.ManagedRuleID,
			Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
			Reference: reference, NonDisclosable: true,
		}}
	})
	asks := 0
	screen := NewModelSecretScreen(m, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		asks++
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:           api.MessageRoleAssistant,
			ModelReasoning: &api.ModelReasoning{Text: "reuse " + generated + " for the retry"},
		}},
		Debug: modelcall.RequestDebug{SessionID: "root-1", RootSessionID: "root-1"},
	}
	got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen non-disclosable reasoning", err)
	if asks != 0 {
		t.Fatalf("asks = %d, want a protected value withheld without an offer", asks)
	}
	if reasoning := got.Messages[0].ModelReasoning; reasoning != nil {
		t.Fatalf("provider-bound reasoning retained a protected value: %+v", reasoning)
	}
}

// Screening and redaction cover the same request fields.
func TestModelSecretScreenInspectsEveryFieldItWouldRedact(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message api.Message
	}{
		{"content_parts", api.Message{Role: api.MessageRoleUser, ContentParts: []api.MessageContentPart{
			{Content: "TOKEN=" + modelScreenGitHubToken},
		}}},
		{"workflow_feedback", api.Message{Role: api.MessageRoleUser, WorkflowFeedback: &api.WorkflowFeedbackMeta{
			Answer: "use " + modelScreenGitHubToken,
		}}},
		{"worker_summary", api.Message{Role: api.MessageRoleAssistant, WorkerSummary: &api.WorkerSummaryMeta{
			Envelope: "wrote TOKEN=" + modelScreenGitHubToken,
		}}},
		{"tool_result_overlay_promotion", api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "promote_overlay", OverlayPromotion: &api.OverlayPromotion{Files: []api.FileEditSnapshot{{Path: "app.env", After: "TOKEN=" + modelScreenGitHubToken}}},
		}}},
		{"tool_result_file_edit", api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			Tool: "edit", FileEdit: &api.FileEditSnapshot{Path: "app.env", After: "TOKEN=" + modelScreenGitHubToken},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asks := 0
			screen := NewModelSecretScreen(modelScreenMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
				asks++
				return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
			})
			got, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, modelcall.CompletionRequest{
				Messages: []api.Message{tc.message}, Debug: modelcall.RequestDebug{SessionID: "sess-1"},
			})
			testutil.FailErr(t, "screen "+tc.name, err)
			if asks != 1 {
				t.Fatalf("asks = %d, want one card before this field reaches a provider", asks)
			}
			raw, err := json.Marshal(got)
			testutil.FailErr(t, "marshal screened request", err)
			if strings.Contains(string(raw), modelScreenGitHubToken) {
				t.Fatalf("provider-bound request retained the token: %s", raw)
			}
		})
	}
}

func TestModelSecretScreenUsesStructuredAuthorizationContext(t *testing.T) {
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
			Name: "request", Args: map[string]any{"Authorization": "Bearer " + modelScreenJWT},
		}}}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1"},
	}
	var finding secretmatch.Alert
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	redacted, err := screen.Screen(context.Background(), ScreenDestination{ID: "provider", Label: "Provider"}, req)
	testutil.FailErr(t, "screen structured Authorization", err)
	value := redacted.Messages[0].ToolCalls[0].Args["Authorization"].(string)
	if value != "Bearer [REDACTED]" {
		t.Fatalf("Authorization = %q", value)
	}
	if finding.RuleID != "authorization-bearer-jwt" || finding.SourceKind != secretmatch.SourceToolCall {
		t.Fatalf("finding = %+v", finding)
	}
}

// RequestDebug supplies project attribution without curation context.
func TestModelSecretScreenCarriesProjectDirFromRequestDebug(t *testing.T) {
	req := modelSecretRequest()
	req.Debug.ProjectDir = "/work/demo"
	var finding secretmatch.Alert
	var contextAttribution secretmatch.AskAttribution
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(ctx context.Context, got secretmatch.Alert) (secretmatch.Resolution, error) {
		finding = got
		contextAttribution = secretmatch.AskAttributionFrom(ctx)
		return secretmatch.Resolution{Decision: secretmatch.SendRedacted}, nil
	})
	_, err := screen.Screen(context.Background(), ScreenDestination{ID: "fireworks-main", Label: "Fireworks"}, req)
	testutil.FailErr(t, "screen with project attribution", err)
	if finding.ProjectDir != "/work/demo" || finding.ProjectID != "proj-1" {
		t.Fatalf("project attribution = %q/%q, want /work/demo/proj-1", finding.ProjectDir, finding.ProjectID)
	}
	if contextAttribution.ProjectDir != "/work/demo" || contextAttribution.ProjectID != "proj-1" ||
		contextAttribution.RootSessionID != "sess-1" {
		t.Fatalf("screen context attribution = %+v", contextAttribution)
	}
	if len(finding.Fingerprints) == 0 {
		t.Fatal("no fingerprints: a release lease could not be minted")
	}
}

func TestRedactMessageForStorageCoversTranscriptFieldsWithoutMutatingSource(t *testing.T) {
	original := api.Message{
		Role:    api.MessageRoleTool,
		Content: "token=" + modelScreenGitHubToken,
		ContentParts: []api.MessageContentPart{{
			Content: "token=" + modelScreenGitHubToken,
			Origin:  api.MessageOriginTool,
		}},
		ToolResult: &api.ToolResult{
			Content:        "token=" + modelScreenGitHubToken,
			DisplaySubject: "command --token=" + modelScreenGitHubToken,
			ToolArgs: map[string]any{
				"Authorization":        "Bearer " + modelScreenJWT,
				modelScreenGitHubToken: "key secret",
			},
			Visual: &api.VisualArtifact{Caption: "token=" + modelScreenGitHubToken},
		},
		ToolCalls: []api.ToolCall{{
			Name:         "request",
			Args:         map[string]any{"token": modelScreenGitHubToken},
			ExtraContent: map[string]any{"Authorization": "Bearer " + modelScreenJWT},
		}},
	}
	stored, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), original)
	if !changed {
		t.Fatal("expected storage redaction")
	}
	storedText := strings.Join([]string{
		stored.Content,
		stored.ContentParts[0].Content,
		stored.ToolResult.Content,
		stored.ToolResult.DisplaySubject,
		stored.ToolResult.ToolArgs["Authorization"].(string),
		stored.ToolResult.Visual.Caption,
		stored.ToolCalls[0].Args["token"].(string),
		stored.ToolCalls[0].ExtraContent["Authorization"].(string),
	}, " ")
	if strings.Contains(storedText, modelScreenGitHubToken) || strings.Contains(storedText, modelScreenJWT) {
		t.Fatalf("storage copy retained a secret: %q", storedText)
	}
	if _, ok := stored.ToolResult.ToolArgs[modelScreenGitHubToken]; ok {
		t.Fatal("storage copy retained a secret-bearing map key")
	}
	if !strings.Contains(original.Content, modelScreenGitHubToken) ||
		!strings.Contains(original.ToolResult.ToolArgs["Authorization"].(string), modelScreenJWT) {
		t.Fatal("storage redaction mutated the transient source")
	}
	if stored.HostSecretRedaction == nil || stored.HostSecretRedaction.Occurrences() != 9 {
		t.Fatalf("storage provenance = %+v want 9 replaced spans", stored.HostSecretRedaction)
	}
}

func TestRedactMessageForStorageCoversWorkflowAndCheckpointProjections(t *testing.T) {
	secret := modelScreenGitHubToken
	before := "before " + secret
	original := api.Message{
		Role:             api.MessageRoleTool,
		WorkflowBoundary: &api.WorkflowBoundaryMeta{Phase: "phase " + secret, Reason: "reason " + secret},
		ProgressUpdate:   &api.ProgressUpdateMeta{Steps: []api.ProgressStep{{Label: "step " + secret}}},
		IndexWarming:     &api.IndexWarmingMeta{Topic: "topic " + secret, Hosts: []string{"host-" + secret}},
		Blueprint:        &api.BlueprintMeta{BlueprintPath: "path-" + secret, BlueprintTitle: "plan " + secret},
		WorkflowFeedback: &api.WorkflowFeedbackMeta{
			Prompt: "enter " + secret, Options: []string{"use " + secret}, Answer: secret,
			Secret: &api.SecretInputMeta{Name: "name " + secret, Purpose: "purpose " + secret},
		},
		ToolResult: &api.ToolResult{
			Content:          "ok",
			Feedback:         []api.ToolFeedback{{Details: map[string]any{"note": secret}}},
			FileEdit:         &api.FileEditSnapshot{Path: "path-" + secret, Before: &before, After: "after " + secret},
			OverlayPromotion: &api.OverlayPromotion{Files: []api.FileEditSnapshot{{Path: "path-" + secret, Before: &before, After: "after " + secret}}},
			ExternalAccess:   &api.ExternalAccess{DeclaredDestinations: []string{"https://" + secret}},
			Skill:            &api.SkillActivation{Instructions: "use " + secret, Resources: []string{"resource-" + secret}},
			CheckpointDecision: &api.CheckpointDecisionMeta{
				Guidance: "retry with " + secret, Subject: secret, CausingCommand: "send " + secret,
			},
		},
		WorkerSummary:  &api.WorkerSummaryMeta{Envelope: "result " + secret, SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: "p", RootID: "r", Path: "worker-" + secret, EntryKind: api.NavigationEntryKindFile}}}},
		SourceContext:  &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: "p", RootID: "r", Path: "source-" + secret, EntryKind: api.NavigationEntryKindFile}}},
		NavigationRefs: []api.NavigationReference{{Mention: "mention " + secret, Path: "path-" + secret}},
	}
	stored, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), original)
	if !changed {
		t.Fatal("expected structured storage redaction")
	}
	raw, err := json.Marshal(stored)
	testutil.FailErr(t, "marshal screened message", err)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("structured message retained secret: %s", raw)
	}
	if original.ToolResult.OverlayPromotion.Files[0].After != "after "+secret {
		t.Fatal("storage redaction mutated promotion snapshots")
	}
	if original.WorkflowFeedback.Answer != secret || original.ToolResult.CheckpointDecision.Guidance != "retry with "+secret {
		t.Fatal("structured storage redaction mutated the source")
	}
}

func TestProjectOnlyModelRequestScreensManagedValuesWithoutATask(t *testing.T) {
	const value = "fixture-project-only-protected-value"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a151}}"
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	screen.SetManagedSecretEvidence(func(_ context.Context, projectID, root string) ([]secretmatch.Remembered, error) {
		if projectID != "project-utility" || root != "" {
			t.Fatalf("utility identity = %q/%q", projectID, root)
		}
		return []secretmatch.Remembered{{Secret: value, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true, Reference: reference}}, nil
	})
	req := modelcall.CompletionRequest{Composition: modelcall.CompositionHostUtility, Debug: modelcall.RequestDebug{ProjectID: "project-utility"},
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Explain this file: TOKEN=" + value}}}
	got, err := screen.Screen(t.Context(), ScreenDestination{ID: "test-provider"}, req)
	testutil.FailErr(t, "screen project utility", err)
	if strings.Contains(got.Messages[0].Content, value) || !strings.Contains(got.Messages[0].Content, reference) {
		t.Fatal("project-only provider request did not replace the managed value")
	}
	if !strings.Contains(req.Messages[0].Content, value) {
		t.Fatal("screen mutated source content")
	}
}

func TestRequestEvidenceReplacesStaleManagedReferenceWithoutMutatingHarvest(t *testing.T) {
	const value = "stale-reference-fixture-value"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a151}}"
	matcher := modelScreenMatcher(t)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{
			Secret: value, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
			Source: secretmatch.SourceRememberedMatch, NonDisclosable: true, Reference: reference}}
	})
	ctx := secretmatch.WithScreeningValues(t.Context(), []secretmatch.Remembered{{
		Secret: value, RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
		Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}})
	hits := matcher.ScreenContext(ctx, value)
	if len(hits) != 1 || hits[0].Reference != "" || !hits[0].NonDisclosable {
		t.Fatal("stale harvest reference overrode current retirement")
	}
	original := matcher.ScreenContext(t.Context(), value)
	if len(original) != 1 || original[0].Reference != reference {
		t.Fatal("request-local evidence changed shared harvest memory")
	}
}
