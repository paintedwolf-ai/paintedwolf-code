package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func surveyRegistry(t *testing.T) *tools.StubRegistry {
	t.Helper()
	reg := tools.NewStubRegistry()
	handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("survey classification invoked a tool")
		return "", nil
	}
	for name, lifecycle := range map[string]toolcontract.Lifecycle{
		"git_diff": toolcontract.LifecycleReadOnly,
		"read":     toolcontract.LifecycleReadOnly,
		"edit":     toolcontract.LifecycleEffectAttempt,
	} {
		testutil.FailErr(t, "register "+name, reg.RegisterDefinition(tools.Definition{
			Meta:     tools.ToolMeta{Name: name},
			Contract: toolcontract.Contract{Owner: "survey-test", Lifecycle: lifecycle},
			Handler:  handler,
		}))
	}
	for _, name := range []string{"update_progress", "request_tools"} {
		testutil.FailErr(t, "register "+name, reg.RegisterDefinition(tools.Definition{
			Meta:     tools.ToolMeta{Name: name},
			Contract: toolcontract.Contract{Owner: "survey-test", Lifecycle: toolcontract.LifecycleDBTransaction, SurveyNeutral: true},
			Handler:  handler,
		}))
	}
	return reg
}

func toolRow(assistantID, tool string, outcome api.ToolResultOutcome) api.Message {
	return api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: tool, AssistantMessageID: assistantID, Outcome: outcome,
	}}
}

func TestJudgeBatchByCompletedCallContracts(t *testing.T) {
	reg := surveyRegistry(t)
	history := []api.Message{
		toolRow("a1", "git_diff", api.ToolResultOutcomeCompleted),
		toolRow("a1", "edit", api.ToolResultOutcomeRejected),
		toolRow("a1", "read", api.ToolResultOutcomeCompleted),
		toolRow("a2", "edit", api.ToolResultOutcomeCompleted),
		toolRow("a3", "mcp_thing", api.ToolResultOutcomeCompleted),
		toolRow("a4", "update_progress", api.ToolResultOutcomeCompleted),
		toolRow("a5", "request_tools", api.ToolResultOutcomeCompleted),
		toolRow("a5", "git_diff", api.ToolResultOutcomeCompleted),
	}
	if verdict, names, _ := judgeBatch(reg, history, "a1"); verdict != batchReadOnly || len(names) != 2 {
		t.Fatalf("a1 = %v %v", verdict, names)
	}
	if verdict, _, _ := judgeBatch(reg, history, "a2"); verdict != batchActed {
		t.Fatal("an edit batch acted")
	}
	if verdict, _, _ := judgeBatch(reg, history, "a3"); verdict != batchActed {
		t.Fatal("a tool the registry cannot describe acted")
	}
	if verdict, _, _ := judgeBatch(reg, history, "a4"); verdict != batchNeutral {
		t.Fatal("bookkeeping alone is neutral")
	}
	if verdict, names, _ := judgeBatch(reg, history, "a5"); verdict != batchReadOnly || len(names) != 1 || names[0] != "git_diff" {
		t.Fatalf("bookkeeping beside a read is a read: %v %v", verdict, names)
	}
	if verdict, _, _ := judgeBatch(reg, history, "a9"); verdict != batchNeutral {
		t.Fatal("a batch with no completed call is neutral")
	}
}

func TestSurveyStreakFiresAtEachCapAndResetsOnAction(t *testing.T) {
	st := &promptLoopTurnState{}
	for i := 1; i < SurveyStreakBatches; i++ {
		st.noteBatchLifecycle(batchReadOnly, []string{"git_diff"}, statusCursorInfo{})
		if st.surveyStreakFires() {
			t.Fatalf("fired after %d read-only batches", i)
		}
	}
	st.noteBatchLifecycle(batchReadOnly, []string{"read"}, statusCursorInfo{})
	if !st.surveyStreakFires() {
		t.Fatalf("did not fire at %d batches", SurveyStreakBatches)
	}
	if got := st.readOnlyStreakTools; len(got) != 2 || got[0] != "git_diff" || got[1] != "read" {
		t.Fatalf("streak tools = %v", got)
	}
	st.surveyStreakNudged = st.readOnlyBatches
	if st.surveyStreakFires() {
		t.Fatal("an announced length must not fire again")
	}
	for i := 0; i < SurveyStreakBatches; i++ {
		st.noteBatchLifecycle(batchReadOnly, nil, statusCursorInfo{})
	}
	if !st.surveyStreakFires() {
		t.Fatalf("did not fire again at %d batches", st.readOnlyBatches)
	}
	st.noteBatchLifecycle(batchNeutral, nil, statusCursorInfo{})
	if st.readOnlyBatches != 2*SurveyStreakBatches || !st.surveyStreakFires() {
		t.Fatal("a neutral batch must leave the streak where it was")
	}
	st.noteBatchLifecycle(batchActed, nil, statusCursorInfo{})
	if st.readOnlyBatches != 0 || st.surveyStreakFires() || st.readOnlyStreakTools != nil {
		t.Fatalf("an acting batch must end the streak: %+v", st)
	}
}

func TestStatusCursorProgressionExemptsReadStreak(t *testing.T) {
	st := &promptLoopTurnState{}
	next80 := 80
	next160 := 160
	st.noteBatchLifecycle(batchReadOnly, []string{"git_status"}, statusCursorInfo{
		valid:      true,
		offset:     0,
		nextOffset: &next80,
		paths:      []string{"dir/"},
		groupDepth: 2,
	})
	if st.readOnlyBatches != 1 {
		t.Fatalf("expected 1 read-only batch, got %d", st.readOnlyBatches)
	}

	st.noteBatchLifecycle(batchReadOnly, []string{"git_status"}, statusCursorInfo{
		valid:      true,
		offset:     80,
		nextOffset: &next160,
		paths:      []string{"dir/"},
		groupDepth: 2,
	})
	if st.readOnlyBatches != 1 {
		t.Fatalf("cursor continuation must not increment readOnlyBatches, got %d", st.readOnlyBatches)
	}

	st.noteBatchLifecycle(batchReadOnly, []string{"git_status"}, statusCursorInfo{
		valid:      true,
		offset:     160,
		nextOffset: nil,
		paths:      []string{"dir/"},
		groupDepth: 2,
	})
	if st.readOnlyBatches != 1 {
		t.Fatalf("exhausted cursor continuation must not increment readOnlyBatches, got %d", st.readOnlyBatches)
	}

	st.noteBatchLifecycle(batchReadOnly, []string{"git_diff"}, statusCursorInfo{})
	if st.readOnlyBatches != 2 {
		t.Fatalf("git_diff read must increment readOnlyBatches, got %d", st.readOnlyBatches)
	}
}
