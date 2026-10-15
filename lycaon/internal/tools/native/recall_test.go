package native

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRecallHistoryAndCoordinateReject(t *testing.T) {
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
			"through_ord": 1,
			"message_id":  "m1",
			"format":      "record",
			"body_runes":  1024,
		},
	}, tctx)
	testutil.FailErr(t, "run recall history record", err)
	if !strings.Contains(out, `"format":"record"`) || !strings.Contains(out, "recovered canonical detail") {
		t.Fatalf("unexpected recall history output: %s", out)
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
