package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

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
