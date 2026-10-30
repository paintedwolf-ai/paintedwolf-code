package policyindex

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func observedPolicyMessage(projectID, rootID, path string) api.Message {
	return api.Message{
		Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: projectID, RootID: rootID, Path: path, EntryKind: api.NavigationEntryKindFile}}},
		ToolResult:    &api.ToolResult{Tool: "command", ToolCallID: "call", Invocation: &api.InvocationReceipt{Tool: "command", ToolCallID: "call"}},
	}
}

func TestResolveCurrentUsesStructuredObservationsAndReachedScopes(t *testing.T) {
	m, sess, roots := policyFixture(t)
	first, second := roots.roots[0], roots.roots[1]
	writePolicy(t, first.Path, "AGENTS.md", "Primary policy")
	writePolicy(t, second.Path, "nested/AGENTS.md", "Observed policy")
	writePolicy(t, second.Path, "reached/AGENTS.md", "Reached policy")
	history := []api.Message{
		{ID: "intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Implement"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "edit", Args: map[string]any{"path": "@library/decoy/file.go"}}}},
		observedPolicyMessage(sess.ProjectID, second.ID, "nested/file.go"),
	}
	delivery, err := m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "structured observation policy", err)
	if len(delivery.Manifest.Files) != 2 {
		t.Fatalf("observed policies = %+v", delivery.Manifest.Files)
	}
	for _, message := range delivery.Messages {
		if strings.Contains(message.Content, "decoy") {
			t.Fatal("assistant arguments established policy scope")
		}
	}
	_, _, err = m.ObserveScopes(t.Context(), sess.ID, []agentpolicy.Scope{{RootID: second.ID, RootPath: second.Path, Path: "reached/new.go"}})
	testutil.FailErr(t, "remember reached scope", err)
	delivery, err = m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "reached operation policy", err)
	if len(delivery.Manifest.Files) != 3 {
		t.Fatalf("reached policies = %+v", delivery.Manifest.Files)
	}
	history = append(history, api.Message{ID: "new-intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Explain another thing"})
	delivery, err = m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "new request policy", err)
	if len(delivery.Manifest.Files) != 1 || delivery.Manifest.Files[0].RootID != first.ID {
		t.Fatal("new request retained an older request's working scopes")
	}
}

func TestObservationScopeIsHostBoundAndBounded(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	writePolicy(t, root.Path, "AGENTS.md", "Shared policy")
	var scopes []agentpolicy.Scope
	for i := 0; i < 1000; i++ {
		scopes = append(scopes, agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: fmt.Sprintf("folder-%d/file.go", i)})
	}
	_, _, err := m.ObserveScopes(t.Context(), sess.ID, scopes)
	testutil.FailErr(t, "remember shared policy jurisdictions", err)
	state, ok := m.observed.Load(sess.ID)
	if !ok || len(state.scopes) != 1 || state.scopes[0].Path != "." {
		t.Fatal("raw operand count expanded the retained policy working set")
	}
	for _, scope := range []agentpolicy.Scope{
		{RootID: "detached", RootPath: root.Path, Path: "file.go"},
		{RootID: root.ID, RootPath: t.TempDir(), Path: "file.go"},
		{RootID: root.ID, RootPath: root.Path, Path: "../escape.go"},
	} {
		if _, _, err := m.ObserveScopes(t.Context(), sess.ID, []agentpolicy.Scope{scope}); err == nil {
			t.Fatalf("accepted an unbound policy scope: %+v", scope)
		}
	}
}

func TestPolicyTrustAndReadFailuresHaveSeparateOutcomes(t *testing.T) {
	m, sess, roots := policyFixture(t)
	roots.err = errors.New("root unavailable")
	roots.disabled = true
	delivery, err := m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "disabled policy surface", err)
	if delivery.Status != StatusDisabled || len(delivery.Messages) != 0 {
		t.Fatal("disabled policy surface read repository files")
	}
	roots.disabled = false
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "unavailable policy diagnostics", err)
	if delivery.Status != StatusUnavailable || len(delivery.Messages) != 1 || len(delivery.Messages[0].ContentParts) != 2 {
		t.Fatal("root failure became an empty policy delivery")
	}
	if delivery.Messages[0].ContentParts[1].Authority != api.ContentAuthorityNone {
		t.Fatal("source failure text acquired instruction authority")
	}
	if _, _, err := m.Current(t.Context(), sess, []agentpolicy.Scope{{RootID: roots.roots[0].ID, RootPath: roots.roots[0].Path, Path: "."}}); !errors.Is(err, roots.err) {
		t.Fatal("mutation admission lost the source failure")
	}
	roots.err = nil
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "workspace without policies", err)
	if delivery.Status != StatusNoPolicy {
		t.Fatal("missing policy and disabled trust were conflated")
	}
}

func TestUnavailableReadScopePreservesIndependentPolicyAndEffects(t *testing.T) {
	m, sess, roots := policyFixture(t)
	first, second := roots.roots[0], roots.roots[1]
	roots.roots[0].Path = filepath.Join(first.Path, "unavailable")
	writePolicy(t, second.Path, "AGENTS.md", "Independent policy")
	history := []api.Message{
		{ID: "request", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, Content: "Implement"},
		observedPolicyMessage(sess.ProjectID, second.ID, "file.go"),
	}
	delivery, err := m.ResolveCurrent(t.Context(), sess, history)
	testutil.FailErr(t, "partial policy delivery", err)
	if delivery.Status != StatusUnavailable || len(delivery.Manifest.Files) != 1 || !strings.Contains(delivery.Messages[0].Content, "Independent policy") {
		t.Fatal("one unavailable scope discarded independent guidance")
	}
	check, err := m.Bind(t.Context(), sess, delivery.Messages)
	testutil.FailErr(t, "bind independent policy", err)
	testutil.FailErr(t, "independent mutation alongside unavailable read", check(t.Context(), []agentpolicy.Target{
		{RootID: first.ID, Path: "file.go"},
		{RootID: second.ID, Path: "file.go", Mutation: true},
	}))
	policyReject(t, check(t.Context(), []agentpolicy.Target{{RootID: first.ID, Path: "file.go", Mutation: true}}), "POLICY_CONTEXT_UNAVAILABLE")
}
