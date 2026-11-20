package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestNativeCommandRulesFollowReachedArgv(t *testing.T) {
	for _, effect := range []settings.ApprovalEffect{settings.ApprovalEffectAsk, settings.ApprovalEffectDeny} {
		t.Run(string(effect), func(t *testing.T) {
			stageBundledApprovals(t, []settings.ApprovalRule{{
				Category: settings.ApprovalCategoryCommand, Pattern: "sort -o*", Effect: effect,
			}})
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create approval store", err)
			approval := settings.NewRuleApprovalGate(store, settings.NoSources())
			action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{
				Tool: "sort", Args: map[string]any{"command": "echo harmless"},
				BoundStages: []toolcontract.ExecutionStage{{
					Argv: []string{"sort", "-o", "sorted.txt", "data.txt"}, CommandLine: "echo harmless",
				}},
			}}
			result, err := approval.Evaluate(t.Context(), action)
			testutil.FailErr(t, "evaluate native rule", err)
			if result.Denied != (effect == settings.ApprovalEffectDeny) || !result.Denied && !result.Required() {
				t.Fatalf("native %s rule was missed: %+v", effect, result)
			}
			if len(result.MatchedRules) != 1 || result.MatchedRules[0].Command != "sort -o sorted.txt data.txt" {
				t.Fatalf("native rule citation = %+v", result.MatchedRules)
			}
			action.Invocation.BoundStages[0].Argv = []string{"sort", "data.txt"}
			action.Invocation.BoundStages[0].CommandLine = "sort -o sorted.txt data.txt"
			action.Invocation.Args["command"] = "sort -o sorted.txt data.txt"
			result, err = approval.Evaluate(t.Context(), action)
			testutil.FailErr(t, "evaluate unmatched argv", err)
			if len(result.MatchedRules) != 0 {
				t.Fatalf("display or arguments supplied command authority: %+v", result.MatchedRules)
			}
		})
	}
}

// A tool rule naming a program entry judges every native child that entry reaches.
func TestToolRulesJudgeTheProgramEntry(t *testing.T) {
	for _, effect := range []settings.ApprovalEffect{settings.ApprovalEffectAsk, settings.ApprovalEffectDeny} {
		t.Run(string(effect), func(t *testing.T) {
			stageBundledApprovals(t, []settings.ApprovalRule{{
				Category: settings.ApprovalCategoryTool, Pattern: "verify", Effect: effect,
			}})
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create approval store", err)
			approval := settings.NewRuleApprovalGate(store, settings.NoSources())
			child := hitl.ProposedAction{Invocation: hitl.ActionInvocation{
				Tool: "go", EntryTool: "verify",
				BoundStages: []toolcontract.ExecutionStage{{Argv: []string{"go", "mod", "init", "example.com/x"}}},
			}}
			result, err := approval.Evaluate(t.Context(), child)
			testutil.FailErr(t, "evaluate entry child", err)
			if result.Denied != (effect == settings.ApprovalEffectDeny) || !result.Denied && !result.Required() {
				t.Fatalf("entry %s rule was missed: %+v", effect, result)
			}
			child.Invocation.EntryTool = "command"
			result, err = approval.Evaluate(t.Context(), child)
			testutil.FailErr(t, "evaluate other entry", err)
			if result.Denied || len(result.MatchedRules) != 0 {
				t.Fatalf("a verify rule judged a command child: %+v", result)
			}
		})
	}
}
