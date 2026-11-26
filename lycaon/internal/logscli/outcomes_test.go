package logscli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/actionoutcome"
	"github.com/lycaon/lycaon/internal/diagnostics"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrintOutcomesFromBundle(t *testing.T) {
	var sessions actionoutcome.Sessions
	s := sessions.For("session")
	ctx, a := actionoutcome.Capture(t.Context(), api.ContinuationActionPlanApproval)
	ticket := s.Receive(ctx, "session", 1, "phase_advanced")
	turnCtx, turn := actionoutcome.BeginTurn(actionoutcome.WithTicket(ctx, ticket), s.Turns, "session", "turn")
	actionoutcome.Dispatch(turnCtx)
	turn.Finish(nil, nil, "")
	a.Commit(s, "session", nil)
	dir := t.TempDir()
	bundle, err := diagnostics.Build(ctx, diagnostics.Input{ConfigDir: dir, LogDir: dir, GeneratedAt: time.Now(), Outcomes: sessions.Trails()})
	testutil.FailErr(t, "build diagnostics", err)
	path := filepath.Join(dir, "diagnostics.zip")
	testutil.FailErr(t, "write bundle", os.WriteFile(path, bundle, 0600))
	var output bytes.Buffer
	testutil.FailErr(t, "print session outcomes", printOutcomes(&output, path, "session"))
	var trail actionoutcome.Trail
	testutil.FailErr(t, "decode outcome trail", json.Unmarshal(output.Bytes(), &trail))
	if len(trail.Wakes) != 1 || len(trail.Turns) != 1 || len(trail.Continuations) != 1 {
		t.Fatalf("trail: %+v", trail)
	}
	w := trail.Wakes[0]
	if w.ContinuationID != a.ID() || w.TurnID != trail.Turns[0].TurnID || trail.Continuations[0].State != api.ContinuationStateContinued {
		t.Fatalf("trail: %+v", trail)
	}
	if err := printOutcomes(&output, path, "absent"); err == nil {
		t.Fatal("read absent session")
	}
}

func TestOutcomesCommandRequiresAnExistingBundleAndSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.zip")
	if err := Run("pw-logs", []string{"outcomes", "--file", path}); err == nil {
		t.Fatal("accepted outcome lookup without a session")
	}
	if err := Run("pw-logs", []string{"outcomes", "--file", path, "--session", "chat"}); !os.IsNotExist(err) {
		t.Fatalf("missing bundle error = %v", err)
	}
}
