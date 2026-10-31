package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	openai "github.com/sashabaranov/go-openai"
)

func TestCloudflarePaidPlanRejectionCompleteAndStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":5035,"message":"private provider diagnostic"}],"success":false}`))
	}))
	defer server.Close()
	entry := CatalogEntry{ID: "cloudflare-workers-ai-1", BaseURL: server.URL + "/accounts/account-1/ai/v1"}
	entry.RejectionReasons = bundledCloudflareRejectionRules(t)
	inner := openaicompat.NewCloudflare(entry.ID, entry.BaseURL, entry.Models, "test-token", nil, server.Client())
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Entries: map[string]CatalogEntry{entry.ID: entry}, Providers: map[string]modelcall.Provider{entry.ID: inner},
	})
	reg := &Registry{}
	reg.outboundSecretScreen = NewModelSecretScreen(nil, nil)
	provider := reg.decorateProvider(snapshot, inner)
	request := modelcall.CompletionRequest{
		Model:    "@cf/zai-org/glm-5.3-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}
	for _, stream := range []bool{false, true} {
		var err error
		if stream {
			_, err = provider.Stream(t.Context(), request)
		} else {
			_, err = provider.Complete(t.Context(), request)
		}
		rejected, ok := providerretry.AsProviderRequestRejected(err)
		if !ok || rejected.Reason != providerretry.RejectionCloudflareWorkersPaidRequired ||
			rejected.ProviderID != entry.ID || rejected.Model != request.Model {
			t.Fatalf("stream=%v: missing paid plan rejection: %v", stream, err)
		}
		var response *openai.RequestError
		if !errors.As(err, &response) {
			t.Fatal("original provider response was not preserved for diagnostics")
		}
	}
}

func TestCloudflareRejectionRequiresStructuredCode(t *testing.T) {
	rules := bundledCloudflareRejectionRules(t)
	for _, body := range []string{
		`{"errors":[{"code":10000,"message":"Workers Paid plan 5035"}]}`,
		`{"errors":[{"message":"Workers Paid plan 5035"}]}`,
		`{"errors":[{"code":5035},{"code":10000}]}`,
		`{"errors":[]}`,
		`Workers Paid plan 5035`,
	} {
		_, err := providerretry.RunProviderAttempts(t.Context(), providerretry.ProviderAttempt{ //nolint:bodyclose // A failed attempt returns no response.
			ProviderID: "fixture",
			Send: func(context.Context) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			},
		})
		rejected, ok := providerretry.AsProviderRequestRejected(err)
		if !ok {
			t.Fatalf("403 did not produce a request rejection: %v", err)
		}
		if got := rejectionReason(err, rules); !errors.Is(got, err) || rejected.Reason != "" {
			t.Fatalf("unclassified response changed: %s", body)
		}
	}
}

func bundledCloudflareRejectionRules(t *testing.T) []ProviderRejectionRule {
	t.Helper()
	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load provider reasons: %v", err)
	}
	ship := map[string]ProviderEntry{}
	for _, provider := range cfg.Providers {
		ship[provider.ID] = provider
	}
	entry, err := resolveLocalEntry(ProviderEntry{ID: "fixture", Kind: "cloudflare-workers-ai"}, ship)
	if err != nil || len(entry.RejectionReasons) != 1 {
		t.Fatalf("inherit Cloudflare reasons: %+v, %v", entry, err)
	}
	return entry.RejectionReasons
}

// A provider entry that never mentions transport still gets the schedule. A
// local provider file replaces http_retry wholesale, so anything it does not
// restate would otherwise be gone.
func TestPolicyWithoutATransportBlockStillRetries(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  3,
		MaxWaitMs:   60000,
		BackoffMs:   []int{1000, 2000, 4000},
		Statuses:    []int{429, 503},
		WaitHeaders: []string{"Retry-After"},
	}
	fault := providerretry.Fault{Kind: providerretry.FaultSilent, Elapsed: 30 * time.Second}
	if !providerretry.ShouldRetryFault(policy, 0, fault) {
		t.Fatal("a policy with no transport block must still reissue a silent request")
	}
	if providerretry.ShouldRetryFault(policy, 1, fault) {
		t.Fatal("the default schedule allows one retry, not two")
	}
	if !providerretry.ShouldRetryFault(policy, 0, providerretry.Fault{Kind: providerretry.FaultEmptyCompletion}) {
		t.Fatal("the default schedule must retry an empty completion before output")
	}
}

func TestStatusFaultsKeepTheirExistingSchedule(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  2,
		MaxWaitMs:   60000,
		BackoffMs:   []int{1000, 2000},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
		Capacity: &providerretry.ProviderHTTPRetry{
			MaxRetries: 2, MaxWaitMs: 60000, BackoffMs: []int{2000, 8000},
			Statuses: []int{503}, WaitHeaders: []string{"Retry-After"},
		},
	}
	for _, tc := range []struct {
		name    string
		status  int
		kind    providerretry.FaultKind
		attempt int
		want    bool
	}{
		{"rate limit first retry", 429, providerretry.FaultRateLimited, 0, true},
		{"rate limit budget spent", 429, providerretry.FaultRateLimited, 2, false},
		{"capacity first retry", 503, providerretry.FaultCapacity, 0, true},
		{"unlisted status never retries", 400, providerretry.FaultStatus, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := providerretry.ShouldRetryFault(policy, tc.attempt, providerretry.Fault{Kind: tc.kind, Status: tc.status})
			if got != tc.want {
				t.Fatalf("ShouldRetryFault(%d, %d) = %v, want %v", tc.attempt, tc.status, got, tc.want)
			}
		})
	}
}

// Every shipped provider must end up with a working response-fault schedule,
// whether it declares one or inherits the default.
func TestEveryShippedProviderRetriesSafeResponseFaults(t *testing.T) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		testutil.FailErr(t, "load shipped providers.yaml", err)
	}
	if len(cfg.Providers) == 0 {
		t.Fatal("shipped providers.yaml declared no providers")
	}
	for _, p := range cfg.Providers {
		if err := providerretry.ValidateHTTPRetry(p.HTTPRetry); err != nil {
			testutil.FailErr(t, "validate http_retry for "+p.ID, err)
		}
		transport := p.HTTPRetry.TransportPolicy()
		if transport.MaxRetries < 1 {
			t.Errorf("provider %s has no transport retry budget", p.ID)
		}
		if !transport.Covers(providerretry.FaultUnreachable) {
			t.Errorf("provider %s would not reissue a request that never reached it", p.ID)
		}
		if !transport.Covers(providerretry.FaultEmptyCompletion) {
			t.Errorf("provider %s would not reissue a safely replayable empty completion", p.ID)
		}
	}
}

// The capacity plane rests a silent slot longer than an overloaded one, because
// silence costs the whole header bound before anything is learned.
func TestShippedCapacityRestsSilenceLongest(t *testing.T) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		testutil.FailErr(t, "load shipped providers.yaml", err)
	}
	policy, err := cfg.Capacity.Policy()
	if err != nil {
		testutil.FailErr(t, "resolve provider_capacity", err)
	}
	if policy.SilentMs <= policy.CooldownMs {
		t.Fatalf("silent cooldown %dms must exceed the overload cooldown %dms",
			policy.SilentMs, policy.CooldownMs)
	}
}

const (
	referencedValue = "orchard-protected-value-7731"
	valueReference  = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}}"
)

func liveManagedEvidence() secretmatch.HarvestedValue {
	return secretmatch.HarvestedValue{
		Secret: referencedValue, Name: "deploy token", Container: "managed secret",
		RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
		Source: secretmatch.SourceRememberedMatch, Reference: valueReference, NonDisclosable: true,
	}
}

// markedText returns the text a span covers in its field value.
func markedText(value string, span api.RedactedSpan) string {
	runes := []rune(value)
	if span.Start < 0 || span.Start+span.Length > len(runes) {
		return ""
	}
	return string(runes[span.Start : span.Start+span.Length])
}

func TestStorageRecordsAReferenceItWritesIntoAToolResult(t *testing.T) {
	matcher := modelScreenMatcher(t)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{liveManagedEvidence()}
	})
	msg := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "read", Content: "1\tSTRIPE_KEY=" + referencedValue + "\n2\tPORT=3000",
	}}

	stored, changed := RedactMessageForStorage(context.Background(), matcher, msg)

	if !changed || stored.ToolResult.Content != "1\tSTRIPE_KEY="+valueReference+"\n2\tPORT=3000" {
		t.Fatalf("stored content = %q changed=%v", stored.ToolResult.Content, changed)
	}
	spans := stored.HostSecretRedaction.SpanList()
	if len(spans) != 1 || spans[0].Kind != api.RedactionKindManagedReference || spans[0].Field != "tool_result.content" {
		t.Fatalf("spans = %+v, want one reference on the tool result", spans)
	}
	if got := markedText(stored.ToolResult.Content, spans[0]); got != valueReference {
		t.Fatalf("span covers %q, want the reference", got)
	}
	if stored.HostSecretRedaction.References() != 1 || stored.HostSecretRedaction.Redactions() != 0 {
		t.Fatalf("counts = %d references, %d redactions", stored.HostSecretRedaction.References(), stored.HostSecretRedaction.Redactions())
	}
	if again, changed := RedactMessageForStorage(context.Background(), matcher, stored); changed ||
		len(again.HostSecretRedaction.SpanList()) != 1 {
		t.Fatalf("rescreening an unchanged copy changed=%v spans=%+v", changed, again.HostSecretRedaction.SpanList())
	}
}

// A carried marker still covers its text after new replacements before it.
func TestRescreenMovesEarlierMarkersPastNewReplacements(t *testing.T) {
	const later = "container-harvest-value-5521"
	evidence := []secretmatch.HarvestedValue{liveManagedEvidence()}
	matcher := modelScreenMatcher(t)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue { return evidence })
	msg := api.Message{Role: api.MessageRoleTool, Content: "OTHER=" + later + " KEY=" + referencedValue}

	first, _ := RedactMessageForStorage(context.Background(), matcher, msg)
	evidence = append(evidence, secretmatch.HarvestedValue{
		Secret: later, Name: "OTHER", Container: ".env",
		RuleID: secretmatch.HarvestRuleID, Title: "A value from .env", Source: secretmatch.SourceContainerHarvest,
	})
	second, changed := RedactMessageForStorage(context.Background(), matcher, first)

	if !changed || second.Content != "OTHER=[REDACTED] KEY="+valueReference {
		t.Fatalf("rescreened content = %q changed=%v", second.Content, changed)
	}
	spans := second.HostSecretRedaction.SpanList()
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want the new placeholder and the carried reference", spans)
	}
	for _, span := range spans {
		want := "[REDACTED]"
		if span.Kind == api.RedactionKindManagedReference {
			want = valueReference
		}
		if got := markedText(second.Content, span); got != want {
			t.Fatalf("%s span covers %q, want %q", span.Kind, got, want)
		}
	}
}

func TestModelScreenAnnouncesReferencesItWrites(t *testing.T) {
	screen := NewModelSecretScreen(modelScreenMatcher(t), nil)
	screen.SetManagedSecretEvidence(func(context.Context, string, string) ([]secretmatch.Remembered, error) {
		value := liveManagedEvidence()
		return []secretmatch.Remembered{{
			Secret: value.Secret, Name: value.Name, RuleID: value.RuleID, Title: value.Title,
			Source: value.Source, Reference: value.Reference, NonDisclosable: true,
		}}, nil
	})
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleTool, Content: "STRIPE_KEY=" + referencedValue}},
		Tools:    []tools.ToolMeta{{Name: "deploy", Description: "Deploys with " + referencedValue}},
		Debug:    modelcall.RequestDebug{SessionID: "root-1", RootSessionID: "root-1", ProjectID: "proj-1"},
	}

	got, err := screen.Screen(t.Context(), ScreenDestination{ID: "provider"}, req)
	testutil.FailErr(t, "screen managed value", err)

	if got.Messages[0].HostSecretRedaction.References() != 1 {
		t.Fatalf("message provenance = %+v", got.Messages[0].HostSecretRedaction)
	}
	if !strings.Contains(got.Tools[0].Description, valueReference) {
		t.Fatalf("tool definition = %q", got.Tools[0].Description)
	}
	notice := got.Messages[len(got.Messages)-1]
	if notice.Kind != api.MessageKindHostSecretRedactionNotice || notice.Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("notice = %+v", notice)
	}
}

func TestModelScreenAnnouncesAReferenceOnlyInAToolDefinition(t *testing.T) {
	matcher := modelScreenMatcher(t)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{liveManagedEvidence()}
	})
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "deploy it"}},
		Tools:    []tools.ToolMeta{{Name: "deploy", Description: "Deploys with " + referencedValue}},
	}

	got := redactModelRequest(t.Context(), matcher, req, keepOnlyReferences)

	notice := got.Messages[len(got.Messages)-1]
	if len(got.Messages) != 2 || notice.Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("messages = %+v", got.Messages)
	}
}
