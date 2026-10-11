package hitl_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestProposedActionStructSizeBudgetInvariants asserts that ProposedAction and its
// cohesive domain types remain strictly within the architectural struct field limit (25 fields).
func TestProposedActionStructSizeBudgetInvariants(t *testing.T) {
	typesToCheck := []struct {
		name string
		val  any
	}{
		{"ProposedAction", hitl.ProposedAction{}},
		{"ActionInvocation", hitl.ActionInvocation{}},
		{"ActionScope", hitl.ActionScope{}},
		{"ActionExecution", hitl.ActionExecution{}},
		{"ActionResources", hitl.ActionResources{}},
		{"ActionMutations", hitl.ActionMutations{}},
		{"ActionPresentation", hitl.ActionPresentation{}},
		{"ActionSockets", hitl.ActionSockets{}},
		{"ActionEgress", hitl.ActionEgress{}},
	}

	const maxFieldsLimit = 25
	for _, tc := range typesToCheck {
		t.Run(tc.name, func(t *testing.T) {
			rt := reflect.TypeOf(tc.val)
			if rt.Kind() == reflect.Pointer {
				rt = rt.Elem()
			}
			numFields := rt.NumField()
			if numFields > maxFieldsLimit {
				t.Fatalf("type %s has %d fields, exceeding maximum budget of %d", tc.name, numFields, maxFieldsLimit)
			}
		})
	}
}

// TestProposedActionScopeInvariants verifies session hierarchy and project identity invariants.
func TestProposedActionScopeInvariants(t *testing.T) {
	t.Run("top-level session resolution", func(t *testing.T) {
		action := hitl.ProposedAction{
			Scope: hitl.ActionScope{
				SessionID: "session-root-123",
			},
		}
		if got := action.ChatSession(); got != "session-root-123" {
			t.Fatalf("ChatSession() = %q, want %q", got, "session-root-123")
		}
		if got := action.Scope.ChatSession(); got != "session-root-123" {
			t.Fatalf("Scope.ChatSession() = %q, want %q", got, "session-root-123")
		}
	})

	t.Run("worker subagent delegates to root session", func(t *testing.T) {
		action := hitl.ProposedAction{
			Scope: hitl.ActionScope{
				SessionID:     "worker-session-456",
				RootSessionID: "root-chat-789",
			},
		}
		if got := action.ChatSession(); got != "root-chat-789" {
			t.Fatalf("ChatSession() = %q, want %q", got, "root-chat-789")
		}
		if got := action.Scope.ChatSession(); got != "root-chat-789" {
			t.Fatalf("Scope.ChatSession() = %q, want %q", got, "root-chat-789")
		}
	})

	t.Run("project identity trims whitespace", func(t *testing.T) {
		action := hitl.ProposedAction{
			Scope: hitl.ActionScope{
				ProjectID: "   ",
			},
		}
		if action.HasProjectIdentity() {
			t.Fatalf("HasProjectIdentity() with whitespace = true, want false")
		}
		if action.Scope.HasProjectIdentity() {
			t.Fatalf("Scope.HasProjectIdentity() with whitespace = true, want false")
		}

		action.Scope.ProjectID = "proj-1"
		if !action.HasProjectIdentity() {
			t.Fatalf("HasProjectIdentity() with valid ID = false, want true")
		}
	})
}

// TestProposedActionGrantKeyDomainInvariants ensures cryptographic grant keys
// depend on authoritative execution facts and remain independent of display presentation facts.
func TestProposedActionGrantKeyDomainInvariants(t *testing.T) {
	baseAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "pytest tests/unit"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: "/path/to/project",
			ProjectID:  "proj-xyz",
			SessionID:  "sess-1",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{
				FSJailed: true,
				Egress:   hitl.ContainedEgressDirectIP,
				Roots:    []string{"/path/to/project"},
			},
		},
		Presentation: hitl.ActionPresentation{
			Command:          "pytest tests/unit",
			PresentationTool: "Run Tests",
			EstimatedImpact:  "Runs unit test suite",
		},
	}

	baseKey := hitl.GrantKey(baseAction)
	if baseKey == "" {
		t.Fatalf("GrantKey for base action is empty")
	}

	t.Run("presentation changes do not alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Presentation.Command = "pytest tests/unit (formatted for UI)"
		modified.Presentation.PresentationTool = "Custom Test Runner"
		modified.Presentation.EstimatedImpact = "Different explanatory text"

		key := hitl.GrantKey(modified)
		if key != baseKey {
			t.Fatalf("GrantKey changed on presentation edit: got %q, want %q", key, baseKey)
		}
	})

	t.Run("execution boundary changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Execution.Contained.FSJailed = false

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when FSJailed changed: %q", key)
		}
	})

	t.Run("confinement root changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Execution.Contained.Roots = []string{"/path/to/project", "/extra/root"}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when confinement roots changed: %q", key)
		}
	})

	t.Run("process access changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Execution.ProcessAccess = "signal"
		modified.Execution.ProcessTargets = []hitl.ApprovalTarget{{Kind: "process", Label: "1234"}}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when process access changed: %q", key)
		}
	})

	t.Run("package execution changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Execution.PackageExecution = &packageexec.Execution{
			Manager:   "npm",
			Operation: packageexec.OperationDependencyInstall,
			Packages:  []packageexec.Package{{System: "npm", Name: "express", RequestedVersion: "4.18.2", Status: packageexec.IdentityResolved}},
		}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when package execution changed: %q", key)
		}
	})

	t.Run("mutation changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Mutations.FileChanges = []api.ApprovalFileChange{{
			Path:      "tests/test_new.py",
			Operation: "create",
		}}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when file mutations changed: %q", key)
		}
	})

	t.Run("agent policy changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Mutations.AgentPolicy = []hitl.AgentPolicyTarget{{
			Path: "AGENTS.md",
		}}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when agent policy mutations changed: %q", key)
		}
	})

	t.Run("host resource changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Resources.HostResources = []string{"docker"}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when host resources changed: %q", key)
		}
	})

	t.Run("host resource family changes alter grant key", func(t *testing.T) {
		modified := baseAction
		modified.Resources.HostResourceFamilies = []string{"container_runtime"}

		key := hitl.GrantKey(modified)
		if key == baseKey {
			t.Fatalf("GrantKey failed to change when host resource families changed: %q", key)
		}
	})
}
