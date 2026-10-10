package toolexecution_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// protectedWriteHITL approves every ask except one citing a sensitive
// location, which it declines, and records every request.
type protectedWriteHITL struct {
	asyncHITL
	mu       sync.Mutex
	requests []hitl.CheckpointRequest
	status   map[string]hitl.DecisionStatus
}

func (m *protectedWriteHITL) RequestCheckpoint(_ context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	id := fmt.Sprintf("chk-%d", len(m.requests))
	status := hitl.DecisionStatusApproved
	if slices.Contains(request.Decision.Gates(), api.GateSensitiveLocation) {
		status = hitl.DecisionStatusRejected
	}
	if m.status == nil {
		m.status = map[string]hitl.DecisionStatus{}
	}
	m.status[id] = status
	return &hitl.CheckpointResponse{CheckpointID: id, Status: hitl.DecisionStatusPending}, nil
}

func (m *protectedWriteHITL) PollCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status[id]
	return &hitl.CheckpointResponse{
		CheckpointID: id, Status: status,
		Result: &hitl.DecisionResult{Approved: status == hitl.DecisionStatusApproved},
	}, nil
}

func (m *protectedWriteHITL) sensitiveAsks() []hitl.CheckpointRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []hitl.CheckpointRequest
	for _, request := range m.requests {
		if slices.Contains(request.Decision.Gates(), api.GateSensitiveLocation) {
			out = append(out, request)
		}
	}
	return out
}

// Command output redirected into a credential file reaches the same
// protected-subject review as a native write of that file, for every stream a
// plan can bind and for any pipeline stage; a declined review leaves the file
// untouched.
func TestCommandRedirectIntoCredentialFileAsksAsProtectedWrite(t *testing.T) {
	cases := []struct{ name, line, target string }{
		{"stdout", "echo leaked > .env", ".env"},
		{"append", "echo leaked >> .env", ".env"},
		{"stderr", `sh -c "echo leaked >&2" 2> .env`, ".env"},
		{"pipeline stage", "printf leaked | sort > .env.production", ".env.production"},
		{"key material", "echo leaked > deploy/id_rsa", "deploy/id_rsa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			testutil.FailErr(t, "seed deploy dir", os.MkdirAll(filepath.Join(project, "deploy"), 0o755))
			credential := filepath.Join(project, filepath.FromSlash(tc.target))
			testutil.FailErr(t, "seed credential", os.WriteFile(credential, []byte("TOKEN=SECRET\n"), 0o600))

			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create approval store", err)
			approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
			boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
			registry := tools.NewDefaultRegistry()
			command := &command.CommandTool{
				Runner: hostcmd.NewRunner(), Boundary: boundary,
				Background: bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{}),
			}
			testutil.FailErr(t, "register command", registry.Register("command", command.Run))
			executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")
			manager := &protectedWriteHITL{}
			executor.Approvals.SetCheckpointManager(manager, approvalGate)

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			_, _ = executor.Invoke(ctx, "command", map[string]any{"command": tc.line}, tools.ToolContext{
				Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
					ActiveRootID:        "root",
					SourceWorkspaceKind: api.SourceWorkspaceKindProject},
				Identity: tools.InvocationIdentity{ProjectID: "project",
					SessionID:  "chat",
					ToolCallID: "redirect-" + tc.name,
					Agent:      "implement"},
			})

			asks := manager.sensitiveAsks()
			if len(asks) == 0 {
				t.Fatalf("%q wrote %s without a %s ask", tc.line, tc.target, api.GateSensitiveLocation)
			}
			action := asks[0].ProposedAction
			if action == nil || len(action.Mutations.FileChanges) == 0 {
				t.Fatalf("%q: the protected ask carries no prepared change: %+v", tc.line, action)
			}
			body, err := os.ReadFile(credential)
			testutil.FailErr(t, "read credential", err)
			if string(body) != "TOKEN=SECRET\n" {
				t.Fatalf("%q: a declined review still changed %s: %q", tc.line, tc.target, body)
			}
		})
	}
}
