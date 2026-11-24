package promptsource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/completioncue"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/repochange"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/loading"
	"github.com/lycaon/lycaon/internal/session/policyindex"
	"github.com/lycaon/lycaon/internal/session/profiles"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/session/turnguards"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubProgressStore struct {
	snap progress.Snapshot
	err  error
}

func (s *stubProgressStore) Set(string, string) error {
	return nil
}

func (s *stubProgressStore) Get(context.Context, string) string {
	return s.snap.Content
}

func (s *stubProgressStore) Snapshot(context.Context, string) (progress.Snapshot, error) {
	return s.snap, s.err
}

func (s *stubProgressStore) Update(context.Context, string, func(progress.Snapshot) (progress.Document, error)) (progress.Snapshot, error) {
	return s.snap, s.err
}

func TestContextBuildWiresCompletionCueCallbacks(t *testing.T) {
	ctx := t.Context()
	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID, sess.WorkspacePath = "project", t.TempDir()

	verif := verification.New(sessions.Sessions, sessions.Transcript, sessionscope.New(sessions.Sessions), nil)
	verif.SetDataDir(t.TempDir())
	verif.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	verif.SetSourceObservations(ledger.Inventory)

	srcFile := filepath.Join(sess.WorkspacePath, "file.txt")
	testutil.FailErr(t, "write file", os.WriteFile(srcFile, []byte("v1"), 0o600))
	repochange.Advance(sess.WorkspacePath)
	rev, root := verif.Revision(ctx, sess.WorkspacePath)
	if rev == "" || root == "" {
		t.Fatal("expected non-empty source revision and root digest")
	}

	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	prog := &stubProgressStore{
		snap: progress.Snapshot{
			Content: "- [x] Done item\n- [ ] Pending one\n- [ ] Pending two\n",
		},
	}

	m := &Context{
		Guards:       &turnguards.Service{},
		Limits:       &sessionlimits.Service{},
		Loading:      &loading.Service{},
		PolicyIndex:  &policyindex.Service{},
		Profiles:     &profiles.Service{},
		Progress:     prog,
		Runtime:      rt,
		Sessions:     sessions.Sessions,
		Verification: verif,
		WorkerState:  &workeroutcomes.State{},
	}

	deps := m.Build()
	if deps.CompletionCues != rt.CompletionCues() {
		t.Fatal("expected Build to wire runtime CompletionCues tracker")
	}

	pending, hasPlan := deps.PendingProgressCount(ctx, sess)
	if !hasPlan || pending != 2 {
		t.Fatalf("PendingProgressCount = (%d, %v), want (2, true)", pending, hasPlan)
	}
	if _, ok := deps.PendingProgressCount(ctx, nil); ok {
		t.Fatal("nil session should report no plan")
	}
	if _, ok := deps.PendingProgressCount(ctx, &api.Session{ID: "   "}); ok {
		t.Fatal("blank session id should report no plan")
	}
	prog.snap = progress.Snapshot{}
	if _, ok := deps.PendingProgressCount(ctx, sess); ok {
		t.Fatal("empty progress snapshot should report no plan")
	}

	fact := completioncue.CheckFact{
		CheckID:          "chk-1",
		SessionID:        sess.ID,
		SourceRevision:   rev,
		SourceRootDigest: root,
	}
	if !deps.ConfirmCheckCurrent(ctx, sess, fact) {
		t.Fatal("expected ConfirmCheckCurrent true for current source")
	}
	if deps.ConfirmCheckCurrent(ctx, nil, fact) {
		t.Fatal("nil session should not confirm check current")
	}
	m.Verification = nil
	if deps.ConfirmCheckCurrent(ctx, sess, fact) {
		t.Fatal("nil verification service should not confirm check current")
	}
}
