package secretcap

import (
	"context"
	"encoding/json"
	"errors"
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
			meta, err := service.Generate(t.Context(), GenerateRequest{
				ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1", OperationID: "delivery",
				Name: "Delivery", Purpose: "delivery test", Scope: ScopeProject,
			})
			testutil.FailErr(t, "generate secret", err)
			resolved, err := service.Resolve(t.Context(), map[string]any{"stdin": meta.Reference}, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ToolName: "command", ToolCallID: "call-delivery"})
			testutil.FailErr(t, "resolve secret", err)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			switch delivery {
			case DeliveryWithheld:
				resolved.Withhold(ctx)
			case DeliveryHandedOff:
				testutil.FailErr(t, "hand off", resolved.HandOff(ctx, nil))
			case DeliveryRedacted:
				resolved.Redacted(nil)
				testutil.FailErr(t, "hand off redacted", resolved.HandOff(ctx, nil))
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
// a project secret, or any other evidence in the same send withholds it. A
// person's value is held whatever travels beside it.
func TestCustodyOfAScreenedSend(t *testing.T) {
	service, _, _ := testService(t)
	matcher := custodyMatcher(t, service)
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

	for name, tt := range map[string]struct {
		refs     []string
		chat     bool
		heldName string
	}{
		"chat generated":         {refs: []string{chat}, chat: true},
		"project generated":      {refs: []string{project}},
		"person entered":         {refs: []string{person.Reference}, heldName: "Person"},
		"chat with person value": {refs: []string{chat, person.Reference}, heldName: "Person"},
	} {
		t.Run(name, func(t *testing.T) {
			resolution, fingerprints := resolveReferences(t, service, matcher, tt.refs...)
			custody := resolution.Custody(fingerprints)
			if custody.ChatGenerated != tt.chat {
				t.Fatalf("ChatGenerated = %v, want %v", custody.ChatGenerated, tt.chat)
			}
			if (tt.heldName == "") != (len(custody.Held) == 0) || (tt.heldName != "" && custody.Held[0].Name != tt.heldName) {
				t.Fatalf("held = %+v, want %q", custody.Held, tt.heldName)
			}
			if tt.chat && resolution.Custody(append(fingerprints, "sf1_raw-detection")).ChatGenerated {
				t.Fatal("a raw detection beside chat values kept chat provenance")
			}
		})
	}
	if summary := (*Resolution)(nil).Custody(nil); summary.ChatGenerated || len(summary.Held) != 0 {
		t.Fatal("a nil resolution reported custody")
	}
}

// Custody lives in the encrypted vault entry, so relabeling a person's value
// in the metadata store neither releases it nor makes it chat generated.
func TestCustodyIgnoresRelabeledMetadata(t *testing.T) {
	service, _, _ := testService(t)
	matcher := custodyMatcher(t, service)
	person, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "relabeled",
		Name: "Relabeled", Purpose: "entered", Value: "person-entered-relabeled",
	})
	testutil.FailErr(t, "create settings secret", err)
	id, err := ParseReference(person.Reference)
	testutil.FailErr(t, "parse reference", err)
	_, err = service.handle.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`)
	testutil.FailErr(t, "allow forged metadata", err)
	_, err = service.handle.ExecContext(t.Context(),
		`UPDATE managed_secrets SET origin = 'generated', scope = 'chat', chat_session_id = 'root-1',
		 created_by_person_id = NULL, format = 'base64url', entropy_bits = 256 WHERE id = ?`, id)
	testutil.FailErr(t, "relabel metadata", err)

	resolution, fingerprints := resolveReferences(t, service, matcher, person.Reference)
	custody := resolution.Custody(fingerprints)
	if custody.ChatGenerated || len(custody.Held) != 1 {
		t.Fatalf("relabeled custody = %+v", custody)
	}
}

// HandOff is the vault's way out: a held value leaves only under an attested
// release, while host values leave under any reviewed one.
func TestHandOffRefusesAnUnattestedHeldValue(t *testing.T) {
	service, _, _ := testService(t)
	matcher := custodyMatcher(t, service)
	person, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "handoff",
		Name: "Deploy key", Purpose: "deploy", Value: "person-entered-handoff",
	})
	testutil.FailErr(t, "create settings secret", err)
	recipient := secretmatch.Recipient{ID: "file:.env", Label: "Local file: .env", Surface: secretmatch.SurfaceFile, Kind: secretmatch.DestinationFile}

	resolution, fingerprints := resolveReferences(t, service, matcher, person.Reference)
	resolution.ApproveRelease(Release{Fingerprints: fingerprints, Recipients: []secretmatch.Recipient{recipient}})
	var unreleased *HeldUnreleasedError
	if err := resolution.HandOff(t.Context(), nil); !errors.As(err, &unreleased) || unreleased.Names[0] != "Deploy key" {
		t.Fatalf("unattested handoff error = %v", err)
	}
	if resolution.UseCovered(fingerprints[0], recipient) {
		t.Fatal("an unattested release covered a held value")
	}

	attested, attestedFingerprints := resolveReferences(t, service, matcher, person.Reference)
	attested.ApproveRelease(Release{Fingerprints: attestedFingerprints, Recipients: []secretmatch.Recipient{recipient}, AttestationID: "11111111-1111-4111-8111-111111111111"})
	testutil.FailErr(t, "attested handoff", attested.HandOff(t.Context(), nil))
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, person.Reference, 0)
	testutil.FailErr(t, "read uses", err)
	if history.Items[0].Delivery != DeliveryHandedOff || history.Items[0].AttestationID != "11111111-1111-4111-8111-111111111111" ||
		len(history.Items[0].Recipients) != 1 || history.Items[0].Recipients[0].Label != "Local file: .env" {
		t.Fatalf("attested use = %+v", history.Items[0])
	}
	if history.Items[1].Delivery != DeliveryWithheld {
		t.Fatalf("refused use = %+v", history.Items[1])
	}
}

func custodyMatcher(t *testing.T, service *Service) *secretmatch.Matcher {
	t.Helper()
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("g", 32)))
	testutil.FailErr(t, "fingerprinter", err)
	service.SetFingerprinter(fingerprinter)
	matcher := secretmatch.NewInertMatcher()
	matcher.SetFingerprinter(fingerprinter)
	return matcher
}

func resolveReferences(t *testing.T, service *Service, matcher *secretmatch.Matcher, refs ...string) (*Resolution, []secretmatch.SecretFingerprint) {
	t.Helper()
	args := map[string]any{"env": map[string]any{}}
	for i, ref := range refs {
		args["env"].(map[string]any)["V"+strconv.Itoa(i)] = ref
	}
	resolution, err := service.Resolve(t.Context(), args, ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", ToolName: "command"})
	testutil.FailErr(t, "resolve", err)
	t.Cleanup(func() { resolution.Finish(t.Context()) })
	matches, err := resolution.Matches(matcher, func(string) bool { return true })
	testutil.FailErr(t, "matches", err)
	return resolution, secretmatch.Fingerprints(matches)
}

// Chat custody is recorded in the vault with its chat: relabeling a host
// value in the metadata store, moving a chat value to another chat, or
// promoting it never yields a silent chat release.
func TestChatCustodyIsBoundInTheVault(t *testing.T) {
	service, _, _ := testService(t)
	matcher := custodyMatcher(t, service)
	project, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1", OperationID: "project-relabeled",
		Name: "Project", Purpose: "provenance", Scope: ScopeProject,
	})
	testutil.FailErr(t, "generate project value", err)
	projectID, err := ParseReference(project.Reference)
	testutil.FailErr(t, "parse project reference", err)
	_, err = service.handle.ExecContext(t.Context(),
		`UPDATE managed_secrets SET scope = 'chat', chat_session_id = 'root-1' WHERE id = ?`, projectID)
	testutil.FailErr(t, "relabel project value as chat", err)
	resolution, fingerprints := resolveReferences(t, service, matcher, project.Reference)
	if resolution.Custody(fingerprints).ChatGenerated {
		t.Fatal("a metadata relabel made a host value chat generated")
	}

	chat, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1", ChatSessionID: "root-1",
		OperationID: "chat-moved", Name: "Chat", Purpose: "provenance", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat value", err)
	chatID, err := ParseReference(chat.Reference)
	testutil.FailErr(t, "parse chat reference", err)
	_, err = service.handle.ExecContext(t.Context(), `UPDATE managed_secrets SET chat_session_id = 'root-2' WHERE id = ?`, chatID)
	testutil.FailErr(t, "move chat value", err)
	moved, err := service.Resolve(t.Context(), map[string]any{"value": chat.Reference},
		ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-2", SessionID: "root-2", ToolName: "command"})
	testutil.FailErr(t, "resolve from the other chat", err)
	t.Cleanup(func() { moved.Finish(t.Context()) })
	matches, err := moved.Matches(matcher, func(string) bool { return true })
	testutil.FailErr(t, "matches", err)
	if moved.Custody(secretmatch.Fingerprints(matches)).ChatGenerated {
		t.Fatal("a chat value moved to another chat kept chat custody there")
	}
	_, err = service.handle.ExecContext(t.Context(), `UPDATE managed_secrets SET chat_session_id = 'root-1' WHERE id = ?`, chatID)
	testutil.FailErr(t, "restore chat value", err)

	scope := ScopeProject
	_, err = service.Update(t.Context(), UpdateRequest{ProjectID: testdbseed.DefaultProjectID, Reference: chat.Reference, Scope: &scope})
	testutil.FailErr(t, "promote", err)
	promoted, promotedFingerprints := resolveReferences(t, service, matcher, chat.Reference)
	if promoted.Custody(promotedFingerprints).ChatGenerated {
		t.Fatal("a promoted value kept chat custody")
	}
	described, err := service.Describe(t.Context(), testdbseed.DefaultProjectID, "root-1", chat.Reference)
	testutil.FailErr(t, "describe promoted", err)
	if described.Custody != CustodyHost {
		t.Fatalf("promoted custody = %q", described.Custody)
	}
}
