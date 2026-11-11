package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A confined program runs in the admitted root and cannot write into an
// attached editor-only root.
func TestConfinedLaunchStaysOutOfEditorOnlyRoots(t *testing.T) {
	script := &citableScriptLLM{}
	h := BuildForTest(t, WithLLMClient(script))
	facts := rootfacts.Install(t)
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, facts.Work)
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, t.Context(), sess.ID)
	h.SeedProgress(t, t.Context(), sess.ID)
	editorOnly := facts.ContainsState
	attachRootOverHTTP(t, h, sess.ProjectID, editorOnly)

	inside := filepath.Join(facts.Work, "admitted.txt")
	outside := filepath.Join(editorOnly, "editor-only.txt")
	script.steps = []citableStep{
		{command: "/usr/bin/touch " + inside},
		{command: "/usr/bin/touch " + outside},
	}
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "create two files")
	testutil.FailErr(t, "prompt", err)

	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("the program did not write in the admitted root: %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("a program wrote into the editor-only root (stat err %v)", err)
	}
	outcomes := sessionProgramOutcomes(t, h, sess.ID)
	if len(outcomes) != 2 || outcomes[0].state != "completed" || outcomes[1].state == "completed" {
		t.Fatalf("outcomes = %+v, want the admitted write completed and the editor-only write stopped", outcomes)
	}
}

func sessionProgramOutcomes(t *testing.T, h *Harness, sessionID string) []programOutcome {
	t.Helper()
	rows, err := h.DB.QueryContext(t.Context(), `SELECT p.state, p.stop_code FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id WHERE r.session_id = ? ORDER BY p.rowid`, sessionID)
	testutil.FailErr(t, "query programs", err)
	defer rows.Close()
	var outcomes []programOutcome
	for rows.Next() {
		var outcome programOutcome
		testutil.FailErr(t, "scan program", rows.Scan(&outcome.state, &outcome.stopCode))
		outcomes = append(outcomes, outcome)
	}
	testutil.FailErr(t, "read programs", rows.Err())
	return outcomes
}
