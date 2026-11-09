package toolexecution_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecutionReviewHonorsReviewedReasonAndLaterDeny(t *testing.T) {
	for _, denyWhileWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("deny-during-review-%t", denyWhileWaiting), func(t *testing.T) {
			stageApprovals(t, settings.ApprovalEffectAsk)
			root := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create policy store", err)
			config := settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk}}}
			testutil.FailErr(t, "install independent ask", store.PutProject(root, config))
			approvals := settings.NewRuleApprovalGate(store, settings.NoSources())
			boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
			registry := tools.NewDefaultRegistry()
			entered := make(chan struct{}, 1)
			testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
				if _, reject := tools.ConfineRequestForSpawn(ctx, tc, nil); reject != nil {
					return "", reject
				}
				entered <- struct{}{}
				return "done", nil
			}))
			executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals), registry, "implement")
			manager := &asyncHITL{requested: make(chan struct{}, 4)}
			executor.Approvals.SetCheckpointManager(t.Context(), manager, approvals)
			ctx, cancel := context.WithTimeout(t.Context(), testutil.Timeout(time.Minute))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := executor.Invoke(ctx, "command", map[string]any{"command": "printf fixture", "capability_request": map[string]any{"host_execution": true}}, tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", Agent: "implement"},
					Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
				})
				done <- err
			}()
			select {
			case <-manager.requested:
			case err := <-done:
				t.Fatalf("returned before required review: %v", err)
			case <-ctx.Done():
				t.Fatal("required review never arrived")
			}
			if manager.request.Decision == nil || !slices.Contains(manager.request.Decision.Gates(), api.GateUserRule) {
				t.Fatalf("review omitted independent ask: %+v", manager.request)
			}
			if len(entered) != 0 {
				t.Fatal("handler entered before review")
			}
			if denyWhileWaiting {
				config.Rules[0].Effect = settings.ApprovalEffectDeny
				testutil.FailErr(t, "install deny during human wait", store.PutProject(root, config))
			}
			manager.approve()
			err = <-done
			if denyWhileWaiting {
				reject := toolrejection.AsToolReject(err)
				if reject == nil || reject.Code != "APPROVAL_RULE_DENIED" || len(entered) != 0 {
					t.Fatalf("late deny did not stop handler: err=%v entered=%d", err, len(entered))
				}
			} else {
				testutil.FailErr(t, "execute reviewed action", err)
				if len(entered) != 1 {
					t.Fatal("reviewed action never reached handler")
				}
			}
			if len(manager.requested) != 0 {
				t.Fatal("same reviewed reason requested another approval")
			}
		})
	}
}
