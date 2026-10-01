package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

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
