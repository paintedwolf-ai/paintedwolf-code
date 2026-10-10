package toolexecution_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// jq_edit is a declared path writer, so a credential file it names as dest
// crosses the sensitive-location gate like any other write: the person sees
// the protected subject and nothing lands before they approve.
func TestJqEditIntoACredentialFileAsksAsASensitiveWrite(t *testing.T) {
	project := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(project, "config.json"), []byte("{\"port\": 8080}\n"), 0o644))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register jq_edit", registry.Register("jq_edit", (&native.JqEditTool{Boundary: boundary}).Run))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "jq_edit", map[string]any{"path": "config.json", "dest": ".env", "query": ".port = 9090"}, tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
				ActiveRootID:        "root",
				SourceWorkspaceKind: api.SourceWorkspaceKindProject},
			Identity: tools.InvocationIdentity{ProjectID: "project",
				SessionID:  "chat",
				ToolCallID: "jq-edit-env",
				Agent:      "implement"},
		})
		done <- err
	}()
	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("jq_edit into .env finished without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("jq_edit into .env did not ask")
	}
	credential := filepath.Join(project, ".env")
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatalf("the credential file changed before approval: %v", err)
	}
	request := manager.request
	if request.Kind != api.CheckpointKindToolApproval || !slices.Contains(request.Decision.Gates(), api.GateSensitiveLocation) {
		t.Fatalf("jq_edit into .env asked %s with gates %v, want a tool approval citing %s",
			request.Kind, request.Decision.Gates(), api.GateSensitiveLocation)
	}

	manager.approve()
	testutil.FailErr(t, "complete the approved write", <-done)
	body, err := os.ReadFile(credential)
	testutil.FailErr(t, "read the credential file", err)
	var landed struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(body, &landed); err != nil || landed.Port != 9090 {
		t.Fatalf("approved bytes did not land: %q (%v)", body, err)
	}
}
