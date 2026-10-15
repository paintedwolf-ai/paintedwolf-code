package native

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRecallHistoryAndCoordinateReject(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	dir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dir, "store.db"))
	sessionID := "coord-recall-1"
	testdbseed.InsertSessionWithRoot(t, sqlDB, sessionID, testdbseed.DefaultProjectID, dir)
	testdbseed.InsertSessionEntry(t, sqlDB, "m1", sessionID, "model_output", "m1", 1)
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO messages (entry_id,id,session_id,role,content,origin,authority,trust_tier,ord,seq,ts)
	VALUES ('m1','m1',?,'assistant','recovered canonical detail','model','none','untrusted',1,1,'2026-10-14T00:00:00Z')`, sessionID)
	testutil.FailErr(t, "seed canonical message", err)

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register recall tool", RegisterRecallTool(reg, recall.NewService(sqlDB, dir)))
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sessionID, ProjectID: testdbseed.DefaultProjectID},
		Effects:  tools.InvocationEffects{Out: &tools.ToolInvocationOut{}},
	}

	out, err := reg.Run(t.Context(), recall.ToolName, map[string]any{
		"history": map[string]any{
			"message_id": "m1",
			"format":     "record",
			"body_runes": 1024,
		},
	}, tctx)
	testutil.FailErr(t, "run recall history record", err)
	if !strings.Contains(out, `"format":"record"`) || !strings.Contains(out, "recovered canonical detail") {
		t.Fatalf("unexpected recall history output: %s", out)
	}

	out, err = reg.Run(t.Context(), recall.ToolName, map[string]any{
		"history": map[string]any{"through_ord": 1, "message_id": "m1", "body_runes": 8},
	}, tctx)
	testutil.FailErr(t, "run bounded recall body", err)
	var page recall.Result
	testutil.FailErr(t, "decode bounded recall body", json.Unmarshal([]byte(out), &page))
	if page.NextRequest == nil || page.NextRequest.History.Offset != 8 || page.NextAction == "" {
		t.Fatal("truncated recall omitted its reachable continuation action")
	}
	encoded, err := json.Marshal(page.NextRequest)
	testutil.FailErr(t, "encode returned recall arguments", err)
	var nextArgs map[string]any
	testutil.FailErr(t, "decode returned recall arguments", json.Unmarshal(encoded, &nextArgs))
	out, err = reg.Run(t.Context(), recall.ToolName, nextArgs, tctx)
	testutil.FailErr(t, "continue recall with returned arguments", err)
	testutil.FailErr(t, "decode continued recall", json.Unmarshal([]byte(out), &page))
	if len(page.History.Entries) != 1 || page.History.Entries[0].Offset != 8 {
		t.Fatal("returned request did not resume the recorded body")
	}

	_, err = reg.Run(t.Context(), recall.ToolName, map[string]any{
		"history": map[string]any{"through_ord": 0},
	}, tctx)
	var rej *toolrejection.ToolReject
	if !errors.As(err, &rej) || rej.Code != recallHistoryInvalid {
		t.Fatalf("expected %s, got %v", recallHistoryInvalid, err)
	}

	_, err = reg.Run(t.Context(), recall.ToolName, map[string]any{}, tctx)
	if !errors.As(err, &rej) || rej.Code != recallQueryInvalid {
		t.Fatalf("expected %s, got %v", recallQueryInvalid, err)
	}
}

func TestRecallRecoveryActionExplainsRecordRequestsAndPreservesExistingAction(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, tc := range []struct {
		name   string
		result recall.Result
		want   string
	}{
		{"index", recall.Result{History: &recall.HistoryPage{Format: "index", Entries: []recall.HistoryEntry{{MessageID: "m"}}}}, "record_request"},
		{"hit", recall.Result{NextAction: "Narrow the query.", Hits: []recall.Hit{{Record: &recall.HistoryRequest{MessageID: "m"}}}}, "record_request"},
		{"body", recall.Result{Hits: []recall.Hit{{Body: []string{"already observed"}}}}, ""},
		{"page", recall.Result{NextRequest: &recall.HistoryContinuation{History: recall.HistoryRequest{MessageID: "m", Offset: 5}}}, "next_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.result.NextAction
			addRecallRecoveryAction(t.Context(), &tc.result)
			if tc.want == "" {
				if tc.result.NextAction != "" {
					t.Fatal("complete observation acquired needless recovery guidance")
				}
				return
			}
			if !strings.Contains(tc.result.NextAction, tc.want) || before != "" && !strings.Contains(tc.result.NextAction, before) {
				t.Fatalf("lost reachable or existing action: %s", tc.result.NextAction)
			}
		})
	}
}
