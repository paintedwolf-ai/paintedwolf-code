package toolexecution_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
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
	for _, scenario := range []struct{ deny, cancel, host, newAsk, missingProducer bool }{
		{host: true}, {deny: true, host: true}, {cancel: true, host: true}, {newAsk: true, host: true}, {missingProducer: true, host: true},
		{}, {deny: true}, {cancel: true}, {newAsk: true}, {missingProducer: true},
	} {
		name := fmt.Sprintf("deny-during-review-%t", scenario.deny)
		if scenario.cancel {
			name = "cancel-during-review"
		}
		if scenario.newAsk {
			name = "new-ask-during-review"
		}
		if scenario.missingProducer {
			name = "explicit-review-of-missing-producer"
		}
		if !scenario.host {
			name += "-confined"
		}
		t.Run(name, func(t *testing.T) {
			stageApprovals(t, settings.ApprovalEffectAsk)
			root := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create policy store", err)
			config := settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk}}}
			testutil.FailErr(t, "install independent ask", store.PutProject(root, config))
			sources := settings.NoSources()
			if scenario.missingProducer {
				sources.Detections = nil
			}
			approvals := settings.NewRuleApprovalGate(store, sources)
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
			manager := &separateReviewHITL{asyncHITL: asyncHITL{requested: make(chan struct{}, 4)}}
			executor.Approvals.SetCheckpointManager(t.Context(), manager, approvals)
			ctx, cancel := context.WithTimeout(t.Context(), testutil.Timeout(time.Minute))
			defer cancel()
			done := make(chan error, 1)
			args := map[string]any{"command": "printf fixture"}
			if scenario.host {
				args["capability_request"] = map[string]any{"host_execution": true}
			}
			go func() {
				_, err := executor.Invoke(ctx, "command", args, tools.ToolContext{
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
			expectedGate := api.GateUserRule
			if scenario.missingProducer {
				expectedGate = api.GateIncompleteFacts
			}
			if manager.request.Decision == nil || !slices.Contains(manager.request.Decision.Gates(), expectedGate) {
				t.Fatalf("review omitted independent ask: %+v", manager.request)
			}
			if len(entered) != 0 {
				t.Fatal("handler entered before review")
			}
			if scenario.deny {
				config.Rules[0].Effect = settings.ApprovalEffectDeny
				testutil.FailErr(t, "install deny during human wait", store.PutProject(root, config))
			}
			if scenario.newAsk {
				config.Rules = append(config.Rules, settings.ApprovalRule{Category: settings.ApprovalCategoryCommand, Pattern: "printf *", Effect: settings.ApprovalEffectAsk})
				testutil.FailErr(t, "add independent command review during wait", store.PutProject(root, config))
			}
			if scenario.cancel {
				cancel()
			} else {
				manager.approve()
			}
			if scenario.newAsk {
				select {
				case <-manager.requested:
				case err := <-done:
					t.Fatalf("new requirement did not reach review: %v", err)
				case <-ctx.Done():
					t.Fatal("new requirement never reached review")
				}
				if len(entered) != 0 || !slices.ContainsFunc(manager.request.ApprovalMatches, func(rule hitl.ApprovalRuleMatch) bool {
					return rule.Category == "command" && rule.Pattern == "printf *"
				}) {
					t.Fatal("new requirement was omitted or dispatched before review")
				}
				manager.approve()
			}
			select {
			case err = <-done:
			case <-manager.requested:
				t.Fatal("unchanged reviewed reasons requested another approval")
			}
			if scenario.cancel {
				if !errors.Is(err, context.Canceled) || len(entered) != 0 || len(approvals.ListGrants("task")) != 0 {
					t.Fatalf("canceled review retained authority or reached handler: err=%v entered=%d", err, len(entered))
				}
			} else if scenario.deny {
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

// Every new checkpoint requires its own human reply; the first reply must not
// make a later checkpoint automatically approved in the fixture.
type separateReviewHITL struct{ asyncHITL }

func (m *separateReviewHITL) RequestCheckpoint(ctx context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.approved.Store(false)
	return m.asyncHITL.RequestCheckpoint(ctx, request)
}
