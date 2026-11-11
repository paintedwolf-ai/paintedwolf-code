package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

func TestModelScreenRefusesAnExceptionWithdrawnDuringReview(t *testing.T) {
	matcher := modelScreenMatcher(t)
	hits := matcher.ScreenContext(t.Context(), modelScreenGitHubToken)
	if len(hits) != 1 {
		t.Fatal("fixture was not detected")
	}
	active := true
	matcher.SetIgnoredSource(func(ctx context.Context) map[secretmatch.SecretFingerprint]bool {
		return map[secretmatch.SecretFingerprint]bool{hits[0].Fingerprint: active && secretmatch.AskAttributionFrom(ctx).ProjectID == "proj-1"}
	})
	asked := false
	screen := NewModelSecretScreen(matcher, func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		asked = true
		active = false
		return secretmatch.Resolution{Decision: secretmatch.SendUnchanged}, nil
	})
	request := modelSecretRequest()
	request.Messages[0].Content += "\nAWS_ACCESS_KEY_ID=AKIAQYJK5TXV4NZR7SGB"
	_, err := screen.Screen(t.Context(), ScreenDestination{ID: "provider"}, request)
	if !asked || !errors.Is(err, ErrModelRequestSecretScreenFailed) {
		t.Fatalf("stale release: asked=%v err=%v", asked, err)
	}
}

// A file tool serializes what it read as a JSON string before any screen sees
// it, so a managed value with a quote or backslash reaches storage in its
// escaped spelling. That spelling is still the value.
func TestStorageReferencesAManagedValueInsideASerializedToolResult(t *testing.T) {
	for name, secret := range map[string]string{
		"backslash":        `Az7\Kp9!Tr2zz`,
		"double quote":     `pa"ss-word-9911`,
		"gcp key fragment": `"private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg"`,
	} {
		t.Run(name, func(t *testing.T) {
			matcher := modelScreenMatcher(t)
			matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
				evidence := liveManagedEvidence()
				evidence.Secret = secret
				return []secretmatch.HarvestedValue{evidence}
			})
			var serialized bytes.Buffer
			encoder := json.NewEncoder(&serialized)
			encoder.SetEscapeHTML(false)
			testutil.FailErr(t, "encode read result", encoder.Encode(map[string]any{
				"path": "config/settings.py", "mode": "content", "content": "1\tDB_PASSWORD=" + secret + "\n2\tPORT=3000",
			}))
			msg := api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read", Content: serialized.String()}}

			stored, changed := RedactMessageForStorage(context.Background(), matcher, msg)

			quoted, _ := json.Marshal(secret)
			if !changed || strings.Contains(stored.ToolResult.Content, strings.Trim(string(quoted), `"`)) {
				t.Fatalf("the escaped spelling reached storage: %s", stored.ToolResult.Content)
			}
			var result map[string]any
			testutil.FailErr(t, "decode stored result", json.Unmarshal([]byte(stored.ToolResult.Content), &result))
			if result["content"] != "1\tDB_PASSWORD="+valueReference+"\n2\tPORT=3000" {
				t.Fatalf("content = %q, want the reference in place of the value", result["content"])
			}
			if stored.HostSecretRedaction.References() != 1 {
				t.Fatalf("references = %d, want the one replacement recorded as a reference", stored.HostSecretRedaction.References())
			}
		})
	}
}

func redactedSpan(kind api.RedactionKind) *api.HostSecretRedactionMeta {
	return api.NewHostSecretRedactionMeta([]api.RedactedSpan{{Field: "content", Start: 0, Length: 10, Kind: kind}})
}

func TestHostSecretRedactionNoticeUsesStructuredProvenance(t *testing.T) {
	literal := []api.Message{{Role: api.MessageRoleTool, Content: "literal [REDACTED] {{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a150}} text"}}
	if got := AppendHostSecretRedactionNotice(literal); len(got) != 1 {
		t.Fatalf("literal marker triggered host notice: %+v", got)
	}

	marked := []api.Message{{
		Role: api.MessageRoleTool, Content: "token=[REDACTED]",
		HostSecretRedaction: redactedSpan(api.RedactionKindSecret),
	}}
	got := AppendHostSecretRedactionNotice(marked)
	if len(got) != 2 || got[1].Role != api.MessageRoleSystem || got[1].Content != HostSecretRedactionNotice(true, false) {
		t.Fatalf("notice projection = %+v", got)
	}
	if got[1].Kind != api.MessageKindHostSecretRedactionNotice || got[1].HostSecretRedaction != nil {
		t.Fatalf("notice identity/provenance = %+v", got[1])
	}
	if again := appendHostSecretRedactionNotice(got, secretReplacements{redactions: 2}); len(again) != len(got) {
		t.Fatalf("notice projection duplicated: %+v", again)
	}
}

func TestHostSecretRedactionNoticeNamesReferencesByKind(t *testing.T) {
	referenced := []api.Message{{Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindManagedReference)}}
	got := AppendHostSecretRedactionNotice(referenced)
	if len(got) != 2 || got[1].Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("reference-only notice = %+v", got)
	}

	definitions := appendHostSecretRedactionNotice([]api.Message{{Role: api.MessageRoleUser}}, secretReplacements{references: 1})
	if len(definitions) != 2 || definitions[1].Content != HostSecretRedactionNotice(false, true) {
		t.Fatalf("tool-definition reference was not announced: %+v", definitions)
	}

	if HostSecretRedactionNotice(false, false) != "" {
		t.Fatal("a notice was written with nothing replaced")
	}
	for _, variant := range []string{HostSecretRedactionNotice(false, true), HostSecretRedactionNotice(true, true)} {
		if variant == HostSecretRedactionNotice(true, false) {
			t.Fatal("a notice naming references reads the same as one naming only redactions")
		}
	}
}

// An earlier projection announces what it saw; a later screen that writes a
// reference restates that one notice rather than adding a second.
func TestHostSecretRedactionNoticeIsRestatedInPlace(t *testing.T) {
	announced := AppendHostSecretRedactionNotice([]api.Message{{
		Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindSecret),
	}})
	later := append(append([]api.Message(nil), announced...), api.Message{
		Role: api.MessageRoleTool, HostSecretRedaction: redactedSpan(api.RedactionKindManagedReference),
	})

	got := AppendHostSecretRedactionNotice(later)

	if len(got) != 3 || got[1].Kind != api.MessageKindHostSecretRedactionNotice || got[1].Content != HostSecretRedactionNotice(true, true) {
		t.Fatalf("restated notice = %+v", got)
	}
	if later[1].Content != HostSecretRedactionNotice(true, false) {
		t.Fatal("restating the notice mutated the caller's messages")
	}
}

func TestRepeatedModelRequestUsesCurrentManagedEvidence(t *testing.T) {
	const value = "latency-fixture-protected-value"
	const reference = "{{paintedwolf-secret:018ff2db-85f7-7f31-8da2-b9e81cd1a151}}"
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: value}},
		Tools: []tools.ToolMeta{{Name: "fixture", Description: value,
			ArgsSchema: map[string]any{"type": "object", "description": value}}},
		Debug: modelcall.RequestDebug{ProjectID: "project", RootSessionID: "root", SessionID: "session"},
	}
	before, err := json.Marshal(req)
	testutil.FailErr(t, "encode original request", err)
	var current []secretmatch.Remembered
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(context.Context, secretmatch.Alert) (secretmatch.Resolution, error) {
		t.Error("a non-disclosable managed value reached approval")
		return secretmatch.Resolution{Decision: secretmatch.Withhold}, nil
	})
	screen.SetManagedSecretEvidence(func(_ context.Context, project, root string) ([]secretmatch.Remembered, error) {
		if project != "project" || root != "root" {
			t.Errorf("evidence attribution = %q/%q", project, root)
		}
		return current, nil
	})
	for _, phase := range []struct {
		name, reference string
		protected       bool
	}{
		{name: "before protection"},
		{name: "new managed value", reference: reference, protected: true},
		{name: "retired reference", protected: true},
		{name: "restored reference", reference: reference, protected: true},
	} {
		t.Run(phase.name, func(t *testing.T) {
			if phase.protected {
				current = []secretmatch.Remembered{{Secret: value, RuleID: secretmatch.ManagedRuleID,
					Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
					NonDisclosable: true, Reference: phase.reference}}
			}
			got, err := screen.Screen(t.Context(), ScreenDestination{ID: "provider", Trusted: true}, req)
			testutil.FailErr(t, "screen repeated request", err)
			wire, err := json.Marshal(got)
			testutil.FailErr(t, "encode screened request", err)
			if strings.Contains(string(wire), value) == phase.protected {
				t.Fatalf("protected=%v, request retained value=%v", phase.protected, strings.Contains(string(wire), value))
			}
			if strings.Contains(string(wire), reference) != (phase.reference != "") {
				t.Fatal("request did not reflect current reference lifecycle")
			}
			after, err := json.Marshal(req)
			testutil.FailErr(t, "encode source request after screening", err)
			if string(after) != string(before) {
				t.Fatal("screening changed the source request")
			}
		})
	}
}

func TestRepeatedModelRequestDoesNotReuseDisclosureDecision(t *testing.T) {
	var destinations []string
	decision := secretmatch.SendUnchanged
	screen := NewModelSecretScreen(modelScreenMatcher(t), func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		destinations = append(destinations, alert.DestinationID)
		return secretmatch.Resolution{Decision: decision}, nil
	})
	req := modelSecretRequest()
	got, err := screen.Screen(t.Context(), ScreenDestination{ID: "first"}, req)
	testutil.FailErr(t, "first approved disclosure", err)
	if !strings.Contains(got.Messages[0].Content, modelScreenGitHubToken) {
		t.Fatal("approved send lost its value")
	}
	decision = secretmatch.Withhold
	got, err = screen.Screen(t.Context(), ScreenDestination{ID: "second"}, req)
	if !errors.Is(err, ErrModelRequestSecretWithheld) || len(got.Messages) != 0 {
		t.Fatalf("repeated request escaped refusal: messages=%d error=%v", len(got.Messages), err)
	}
	if len(destinations) != 2 || destinations[0] != "first" || destinations[1] != "second" {
		t.Fatalf("disclosure destinations = %v", destinations)
	}
}
