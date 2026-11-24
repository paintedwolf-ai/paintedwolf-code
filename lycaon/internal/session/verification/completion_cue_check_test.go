package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckMatchesCurrentSourceAndRecordsIntentAttribution(t *testing.T) {
	ctx := t.Context()
	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID, sess.WorkspacePath = "project", t.TempDir()

	m := New(sessions.Sessions, sessions.Transcript, scope.New(sessions.Sessions), nil)
	m.SetDataDir(t.TempDir())
	evStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	m.SetEvidenceStore(evStore)
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	m.SetSourceObservations(ledger.Inventory)

	if (*Service)(nil).CheckMatchesCurrentSource(ctx, sess, "chk-1", "rev", "root") {
		t.Fatal("nil service reported current source")
	}
	if m.CheckMatchesCurrentSource(ctx, nil, "chk-1", "rev", "root") {
		t.Fatal("nil session reported current source")
	}

	sourcePath := filepath.Join(sess.WorkspacePath, "main.go")
	write := func(content string) {
		testutil.FailErr(t, "write source", os.WriteFile(sourcePath, []byte(content), 0o600))
		repochange.Advance(sess.WorkspacePath)
	}
	write("package main\n")
	revision, root := m.Revision(ctx, sess.WorkspacePath)
	if revision == "" || root == "" {
		t.Fatal("expected non-empty source revision and root digest")
	}

	var observed tools.SourceRunCapture
	var observedProducer string
	m.SetSourceRunObserver(func(_ context.Context, _ *api.Session, producer string, run tools.SourceRunCapture) {
		observedProducer = producer
		observed = run
	})

	m.RecordSourceRun(ctx, sess, ProducerCommand, tools.SourceRunCapture{
		CheckID:          "chk-1",
		IsCheck:          true,
		Command:          "go test ./...",
		Cwd:              sess.WorkspacePath,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   revision,
		SourceRootDigest: root,
		UserIntentID:     "  intent-42  ",
		WorkflowRunID:    "wf-1",
	})

	if observedProducer != ProducerCommand || observed.CheckID != "chk-1" || observed.UserIntentID != "  intent-42  " {
		t.Fatalf("unexpected observer call: producer=%q run=%+v", observedProducer, observed)
	}

	records, err := evStore.Read(ctx, m.EvidenceRootFor(sess))
	testutil.FailErr(t, "read evidence", err)
	if len(records) != 1 || records[0].Artifacts["user_intent_id"] != "intent-42" {
		t.Fatalf("expected trimmed user_intent_id artifact, got %+v", records)
	}

	if !m.CheckMatchesCurrentSource(ctx, sess, "chk-1", revision, root) {
		t.Fatal("expected current check to match unchanged source")
	}
	if m.CheckMatchesCurrentSource(ctx, sess, "chk-1", revision, "wrong-root") {
		t.Fatal("expected mismatched root digest to be rejected")
	}

	write("package main\n// changed\n")
	if m.CheckMatchesCurrentSource(ctx, sess, "chk-1", revision, root) {
		t.Fatal("expected check to be stale after source edit")
	}
}
