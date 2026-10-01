package secretcap

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/ptyinput"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolutionPinsVersionAndKeepsProvenancePrivate(t *testing.T) {
	service, _, _ := testService(t)
	const original = "first-value && still one argument"
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "snapshot", Name: "Snapshot", Purpose: "snapshot test", Value: original,
	})
	testutil.FailErr(t, "create secret", err)
	args := map[string]any{"command": "program --value=" + meta.Reference, "nested": map[string]any{"a/b": []any{meta.Reference}}}
	resolved, err := service.Resolve(t.Context(), args, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1"})
	testutil.FailErr(t, "resolve snapshot", err)
	_, err = service.ReplaceValue(t.Context(), ReplaceValueRequest{ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "rotated-value"})
	testutil.FailErr(t, "rotate stored version", err)
	got, err := resolved.Substitute(meta.Reference)
	testutil.FailErr(t, "insert snapshot", err)
	if got != original {
		t.Fatal("in-flight invocation changed versions")
	}
	next, err := service.Resolve(t.Context(), args, ResolveContext{ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "resolve rotated version", err)
	if next.Arguments["command"] != "program --value=rotated-value" {
		t.Fatal("next invocation did not use rotation")
	}
	encoded, err := json.Marshal(resolved)
	testutil.FailErr(t, "marshal private resolution", err)
	if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%+v", resolved), original) {
		t.Fatal("resolution exposed private execution material")
	}
	if len(resolved.values) != 1 || len(resolved.bindings) != 2 || resolved.bindings[1].path != "/nested/a~1b/0" {
		t.Fatal("resolution lost occurrence paths")
	}
}

func TestDeliveryHistorySeparatesResolutionFromHandoff(t *testing.T) {
	for _, delivery := range []string{DeliveryNotDispatched, DeliveryWithheld, DeliveryHandedOff, DeliveryRedacted} {
		t.Run(delivery, func(t *testing.T) {
			service, _, _ := testService(t)
			meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
				ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "delivery", Name: "Delivery", Purpose: "delivery test", Value: "private-delivery-value",
			})
			testutil.FailErr(t, "create secret", err)
			resolved, err := service.Resolve(t.Context(), map[string]any{"stdin": meta.Reference}, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "command", ToolCallID: "call-delivery"})
			testutil.FailErr(t, "resolve secret", err)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			switch delivery {
			case DeliveryWithheld:
				resolved.Withhold(ctx)
			case DeliveryHandedOff:
				resolved.HandOff(ctx, nil)
			case DeliveryRedacted:
				resolved.Redacted(nil)
				resolved.HandOff(ctx, nil)
			}
			resolved.Finish(ctx)
			history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
			testutil.FailErr(t, "read history", err)
			if len(history.Items) != 1 || history.Items[0].Outcome != UseResolved || history.Items[0].Delivery != delivery || history.Items[0].ToolCallID != "call-delivery" {
				t.Fatalf("incorrect history: %+v", history)
			}
		})
	}
}

func TestReferenceSupportsConcurrentWorkers(t *testing.T) {
	service, _, _ := testService(t)
	service.remember = nil
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "workers", Name: "Worker key",
	})
	testutil.FailErr(t, "generate worker key", err)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			r, err := service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
				ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: fmt.Sprintf("worker-%d", i), ToolCallID: fmt.Sprintf("call-%d", i),
			})
			if err != nil {
				t.Errorf("resolve concurrent worker: %v", err)
				return
			}
			r.HandOff(t.Context(), nil)
			r.Finish(t.Context())
		})
	}
	workers.Wait()
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read worker history", err)
	if len(history.Items) != 8 {
		t.Fatalf("recorded %d worker calls", len(history.Items))
	}
	for _, use := range history.Items {
		if use.Delivery != DeliveryHandedOff {
			t.Fatal("worker handoff was lost")
		}
	}
}

func TestTerminalSecretValueIsNotControlSyntax(t *testing.T) {
	service, _, _ := testService(t)
	const secret = "value{Ctrl-C}{Enter}{{nested}}"
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "terminal-literal", Name: "terminal", Purpose: "literal payload test", Value: secret})
	testutil.FailErr(t, "create terminal secret", err)
	canonical := map[string]any{"input": meta.Reference + "{Enter}"}
	resolved, err := service.Resolve(t.Context(), canonical, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "terminal_send"})
	testutil.FailErr(t, "resolve terminal secret", err)
	input := resolved.Arguments["input"].(string)
	payload, err := ptyinput.ExpandControlInput(input)
	testutil.FailErr(t, "expand terminal payload", err)
	if string(payload) != secret+"\r" {
		t.Fatalf("terminal control grammar changed a protected value")
	}
	if canonical["input"] != meta.Reference+"{Enter}" || !resolved.Binds("/input") {
		t.Fatal("encoding changed canonical input or lost protected provenance")
	}
	if got := resolved.RedactKnown(input).(string); got != "***{Enter}" {
		t.Fatal("encoded terminal value escaped redaction")
	}
}

// Only chat-scoped generated values carry chat provenance; a person's secret,
// a project secret, or any other evidence in the same send withholds it.
func TestGeneratedForChatRequiresEveryMatchToBeAChatGeneratedValue(t *testing.T) {
	service, _, _ := testService(t)
	generate := func(operation, scope string) string {
		req := GenerateRequest{
			ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1",
			OperationID: operation, Name: operation, Purpose: "provenance", Scope: scope,
		}
		if scope == ScopeChat {
			req.ChatSessionID = "root-1"
		}
		meta, err := service.Generate(t.Context(), req)
		testutil.FailErr(t, "generate "+operation, err)
		return meta.Reference
	}
	chat := generate("chat-generated", ScopeChat)
	project := generate("project-generated", ScopeProject)
	person, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "person", Name: "Person", Purpose: "entered", Value: "person-entered-value",
	})
	testutil.FailErr(t, "create settings secret", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("g", 32)))
	testutil.FailErr(t, "fingerprinter", err)
	matcher := secretmatch.NewInertMatcher()
	matcher.SetFingerprinter(fingerprinter)

	for name, tt := range map[string]struct {
		refs []string
		want bool
	}{
		"chat generated":         {[]string{chat}, true},
		"project generated":      {[]string{project}, false},
		"person entered":         {[]string{person.Reference}, false},
		"chat with person value": {[]string{chat, person.Reference}, false},
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"env": map[string]any{}}
			for i, ref := range tt.refs {
				args["env"].(map[string]any)["V"+strconv.Itoa(i)] = ref
			}
			resolution, err := service.Resolve(t.Context(), args, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1"})
			testutil.FailErr(t, "resolve", err)
			defer resolution.Finish(t.Context())
			matches, err := resolution.Matches(matcher, func(string) bool { return true })
			testutil.FailErr(t, "matches", err)
			if got := resolution.GeneratedForChat(matches); got != tt.want {
				t.Fatalf("GeneratedForChat = %v, want %v", got, tt.want)
			}
			if tt.want && resolution.GeneratedForChat(append(matches, secretmatch.Match{RuleID: "gitleaks:generic-api-key"})) {
				t.Fatal("a shape-rule match beside chat values kept chat provenance")
			}
		})
	}
	if (*Resolution)(nil).GeneratedForChat(nil) {
		t.Fatal("a nil resolution reported chat provenance")
	}
}
