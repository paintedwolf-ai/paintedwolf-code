//go:build integration

package toolexecution_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
)

// Satisfying a capability does not freeze discretionary configuration or grant
// exemption from a later deny. Exercise real policy persistence and matching.
func TestBoundaryAuthorityRevalidatesLiveDenyWithoutBlockingRecovery(t *testing.T) {
	for _, satisfied := range []bool{false, true} {
		t.Run(fmt.Sprintf("boundary-satisfied-%t", satisfied), func(t *testing.T) {
			root := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			testutil.FailErr(t, "create policy store", err)
			neverAsk := true
			testutil.FailErr(t, "configure user-owned quiet policy", store.PutGlobal(settings.ApprovalConfig{NeverAsk: &neverAsk}))
			config := settings.ApprovalConfig{}
			boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
				ID: "implement", Tools: map[string]bool{"command": true},
			}})
			engine := toolexecution.NewApprovalPolicyEngine(
				toolprofiles.NewProfilePolicyEngine(boundary),
				settings.NewRuleApprovalGate(store, settings.NoSources()),
			)
			action := platform.PolicyContext{
				ProfileID: "implement", ToolName: "command", ProjectDir: root,
				ToolArgs:                  map[string]any{"command": "printf fixture"},
				BoundaryApprovalSatisfied: satisfied,
			}
			assertDisposition := func(blocked bool) {
				t.Helper()
				decision, err := engine.Evaluate(t.Context(), action)
				testutil.FailErr(t, "evaluate current policy", err)
				if decision == nil || decision.Blocked != blocked {
					t.Fatalf("live deny disposition lost: want blocked=%t, got %+v", blocked, decision)
				}
				if blocked {
					if decision.Allowed || decision.RejectCode != "APPROVAL_RULE_DENIED" {
						t.Fatalf("deny did not retain its enforcement identity: %+v", decision)
					}
				} else if !decision.Allowed || decision.RequiresApproval {
					t.Fatalf("covered work failed to proceed quietly: %+v", decision)
				}
			}
			assertDisposition(false)
			config.Rules = []settings.ApprovalRule{{
				Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectDeny,
			}}
			testutil.FailErr(t, "install live deny", store.PutProject(root, config))
			assertDisposition(true)
			config.Rules = nil
			testutil.FailErr(t, "remove only the fixture deny", store.PutProject(root, config))
			assertDisposition(false)
		})
	}
}
