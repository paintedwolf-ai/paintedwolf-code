package toolexecution_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestExecutionCapabilityAsksBeforeSpawnAndConsumesOnce(t *testing.T) {
	for _, capability := range []string{"process_control", "host_execution"} {
		t.Run(capability, func(t *testing.T) {
			if capability == "process_control" && !confine.Available() {
				t.Skip("process control requires an available sandbox")
			}
			confine.TestingSetAutoConfine(t)
			stageApprovals(t, settings.ApprovalEffectAsk)
			root := t.TempDir()
			branch := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create approval store", err)
			gate := settings.NewRuleApprovalGate(store, settings.NoSources())
			boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
			registry := tools.NewDefaultRegistry()
			entered := make(chan struct{}, 16)
			testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
				entered <- struct{}{}
				req, reject := tools.ConfineRequestForSpawn(ctx, tc, nil)
				if reject != nil {
					return "", reject
				}
				if req.HostExecution != (capability == "host_execution") || req.ProcessControl != (capability == "process_control") {
					t.Errorf("incorrect launch request: %+v", req)
				}
				if len(req.Roots) != 1 || req.Roots[0] != branch {
					t.Errorf("reviewed launch did not use worker branch: %+v", req.Roots)
				}
				if _, reject = tools.ConfineRequestForSpawn(ctx, tc, nil); reject == nil {
					t.Error("launch permit was reusable")
				}
				return "done", nil
			}))
			executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), registry, "implement")
			manager := &asyncHITL{requested: make(chan struct{}, 4)}
			executor.Approvals.SetCheckpointManager(manager, gate)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root", WorkerJobID: "worker", WorkerCoord: executionWorkerBranch{root: branch}, SessionID: "task", ToolCallID: "call", Agent: "implement"}
			go func() {
				_, err := executor.Invoke(ctx, "command", map[string]any{"command": "sudo -n /usr/bin/id", "capability_request": map[string]any{capability: true}}, tc)
				done <- err
			}()

			select {
			case <-manager.requested:
			case err := <-done:
				t.Fatalf("returned without approval: %v", err)
			case <-ctx.Done():
				t.Fatal("approval not requested")
			}
			select {
			case <-entered:
				t.Fatal("handler entered before approval")
			default:
			}
			plan, err := hitl.CompileCheckpointApprovalPlan(manager.request)
			testutil.FailErr(t, "compile standard approval", err)
			if string(plan.Subject.Kind) != capability {
				t.Fatalf("subject: %s", plan.Subject.Kind)
			}
			manager.approve()
			testutil.FailErr(t, "run approved invocation", <-done)
			if len(manager.requested) != 0 {
				t.Fatal("duplicate approval")
			}
			installed := false
			for _, option := range plan.Options {
				if option.ID != plan.RecommendedOptionID {
					continue
				}
				for _, delta := range option.Authority {
					if delta.Grant != nil && delta.Grant.Predicate.Category == hitl.ApprovalGrantCategoryExecutionCapability {
						_, err := gate.ApplyGrant(*delta.Grant)
						testutil.FailErr(t, "install approved task permission", err)
						installed = true
					}
				}
			}
			if !installed {
				t.Fatal("primary option did not offer task capability")
			}
			for i := range 15 {
				tc.ToolCallID = fmt.Sprintf("next-%d", i)
				_, err := executor.Invoke(ctx, "command", map[string]any{"command": fmt.Sprintf("sudo -n /usr/bin/printf %d", i), "capability_request": map[string]any{capability: true}}, tc)
				testutil.FailErr(t, "run different command under task permission", err)
			}
			if len(manager.requested) != 0 {
				t.Fatal("task permission asked again for different commands")
			}

		})
	}
}

type executionWorkerBranch struct {
	tools.WorkerWriteCoordinator
	root string
}

func (b executionWorkerBranch) EnsureWorkerBranch(_ context.Context, tc tools.ToolContext) (tools.ToolContext, error) {
	tc.WorkerBranchRoot = b.root
	return tc, nil
}

// Globs expand once, in the worker branch the call runs on, before the
// capability card; the card and the handler read the same argv.
func TestCapabilityCardAndExecutionShareTheBranchExpandedArgv(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	root, branch := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write primary file", os.WriteFile(filepath.Join(root, "primary.txt"), nil, 0o600))
	for _, name := range []string{"a.txt", "b.txt"} {
		testutil.FailErr(t, "write branch file", os.WriteFile(filepath.Join(branch, name), nil, 0o600))
	}
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	ran := make(chan string, 1)
	testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if _, reject := tools.ConfineRequestForSpawn(ctx, tc, nil); reject != nil {
			return "", reject
		}
		command, _ := args["command"].(string)
		ran <- command
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), registry, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 4)}
	executor.Approvals.SetCheckpointManager(manager, gate)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tc := tools.ToolContext{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root", WorkerJobID: "worker", WorkerCoord: executionWorkerBranch{root: branch}, SessionID: "task", ToolCallID: "call", Agent: "implement"}
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "command", map[string]any{"command": "ls *.txt", "capability_request": map[string]any{"host_execution": true}}, tc)
		done <- err
	}()

	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("returned without approval: %v", err)
	case <-ctx.Done():
		t.Fatal("approval not requested")
	}
	card, err := json.Marshal(manager.request)
	testutil.FailErr(t, "encode card", err)
	if !strings.Contains(string(card), "ls a.txt b.txt") || strings.Contains(string(card), "primary.txt") {
		t.Fatalf("capability card does not state the branch-expanded argv: %s", card)
	}
	manager.approve()
	testutil.FailErr(t, "run approved invocation", <-done)
	if got := <-ran; got != "ls a.txt b.txt" {
		t.Fatalf("executed command = %q, want the argv the card stated", got)
	}
}
