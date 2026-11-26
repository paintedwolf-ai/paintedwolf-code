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
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "diagnostics.zip")
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := printOutcomes(&output, path, "session"); err != nil {
		t.Fatal(err)
	}
	var trail actionoutcome.Trail
	if err := json.Unmarshal(output.Bytes(), &trail); err != nil {
		t.Fatal(err)
	}
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
