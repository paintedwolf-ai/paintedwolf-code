package policyindex

import (
	"errors"
	"fmt"
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
	testutil.FailErr(t, "remember reached scope", m.Observe(t.Context(), sess.ID, agentpolicy.Scope{RootID: second.ID, RootPath: second.Path, Path: "reached/new.go"}))
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
	for i := 0; i < maxObservedScopes+10; i++ {
		testutil.FailErr(t, "remember scope", m.Observe(t.Context(), sess.ID, agentpolicy.Scope{RootID: root.ID, RootPath: root.Path, Path: fmt.Sprintf("folder-%d/file.go", i)}))
	}
	state, ok := m.observed.Load(sess.ID)
	if !ok || len(state.scopes) != maxObservedScopes || state.scopes[0].Path != fmt.Sprintf("folder-%d/file.go", maxObservedScopes+9) {
		t.Fatal("observed scopes did not retain a bounded recent working set")
	}
	for _, scope := range []agentpolicy.Scope{
		{RootID: "detached", RootPath: root.Path, Path: "file.go"},
		{RootID: root.ID, RootPath: t.TempDir(), Path: "file.go"},
		{RootID: root.ID, RootPath: root.Path, Path: "../escape.go"},
	} {
		if err := m.Observe(t.Context(), sess.ID, scope); err == nil {
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
	if _, err := m.ResolveCurrent(t.Context(), sess, nil); !errors.Is(err, roots.err) {
		t.Fatal("root failure became an empty policy delivery")
	}
	roots.err = nil
	delivery, err = m.ResolveCurrent(t.Context(), sess, nil)
	testutil.FailErr(t, "workspace without policies", err)
	if delivery.Status != StatusNoPolicy {
		t.Fatal("missing policy and disabled trust were conflated")
	}
}
