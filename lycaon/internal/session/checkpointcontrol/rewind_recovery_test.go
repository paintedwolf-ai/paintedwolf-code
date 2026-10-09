package checkpointcontrol

import (
	"context"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"
)

// Startup reclaims anchors left behind after the rewind commit.
func TestSweepCommittedRewindsReclaimsOrphanedAnchor(t *testing.T) {
	rewinds, repository, ledger, sessionID, dir := newRewindControlFixture(t)
	owner, err := repository.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	ctx := people.WithCaller(t.Context(), owner)

	anchor := api.Message{
		ID: "u-sweep", Role: api.MessageRoleUser, Content: "change file",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", repository.AppendMessages(ctx, sessionID, anchor))

	path := filepath.Join(dir, "file.txt")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte("before"), 0o640))
	cpStore := sessioncheckpoint.New(rewinds.captures.dataDir, dir, repository)
	_, err = cpStore.Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "file.txt"))
	testutil.FailErr(t, "turn writes", os.WriteFile(path, []byte("after"), 0o640))
	project, err := rewinds.rewindProject(ctx, sessionID)
	testutil.FailErr(t, "resolve project", err)
	turn, err := repository.UserTurnOrdinal(ctx, sessionID)
	testutil.FailErr(t, "resolve turn", err)
	testutil.FailErr(t, "record effect", ledger.Record(ctx, sourceledger.RecordInput{ProjectID: project.ID, RootID: project.Roots[0].ID, Path: "file.txt", Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: sessionID, Turn: turn, Before: []byte("before"), After: []byte("after")}))

	rootID := sessiontree.RootID(ctx, repository, sessionID)
	// The existence check below fails loudly if this checkpoint layout changes.
	anchorDir := filepath.Join(enginepaths.ProjectCheckpointDir(enginepaths.SessionCheckpointsRootUnder(rewinds.captures.dataDir), filepath.Clean(dir)), "anchors", rootID, anchor.ID)
	if _, err := os.Stat(anchorDir); err != nil {
		t.Fatalf("anchor dir missing before rewind: %v", err)
	}

	msgs, err := repository.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	anchorIDs := eligibleAnchorIDsFrom(msgs, anchor.ID)
	operationID := uuid.NewString()
	result := &RewindResult{}
	// Stop after commit to leave anchor cleanup pending.
	_, err = rewinds.executeRewind(ctx, cpStore, rootID, operationID, rewindInputDigest(sessionID, anchor.ID), sessionID, anchor.ID, anchorIDs, controlPreviewDigest(t, rewinds, ctx, sessionID, anchor.ID), result)
	testutil.FailErr(t, "execute rewind", err)

	if _, err := os.Stat(anchorDir); err != nil {
		t.Fatalf("anchor dir missing right after commit (test setup invalid): %v", err)
	}

	testutil.FailErr(t, "sweep committed rewinds", rewinds.sweepCommittedRewinds(ctx))

	if _, err := os.Stat(anchorDir); !os.IsNotExist(err) {
		t.Fatalf("anchor dir still present after sweep, err=%v", err)
	}

	// Retained receipts remain available for retries.
	op, err := repository.GetRewindOperation(ctx, operationID)
	testutil.FailErr(t, "get rewind operation", err)
	if op == nil || op.Status != "committed" {
		t.Fatalf("operation = %+v, want still committed (within retention)", op)
	}
}

func newRewindControlFixture(t *testing.T) (*Rewinds, *sessionstore.SQL, *sourceledger.Store, string, string) {
	t.Helper()
	database := testdbfixture.Open(t, "session.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	repository := sessionstore.NewSQL(database)
	projects := project.NewSQLRegistry(database)
	workspace := sessionscope.New(repository)
	workspace.SetProjects(projects)
	captures := NewCapture(t.TempDir(), repository, workspace)
	ledger := sourceledger.New(database, filepath.Join(captures.dataDir, "source-content"))
	mutations := projectsource.NewSourceMutationService(database, ledger)
	rewinds := NewRewinds(repository, captures, &promptstate.MutexRegistry{}, workspace, projects, &sourcerewind.Service{Ledger: ledger, Mutations: mutations}, nil, Runtime{WorkersInFlight: func(context.Context, *api.Session) int { return 0 }})
	session, err := repository.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "bind workspace", repository.UpdateSession(t.Context(), session.ID, func(s *api.Session) { s.WorkspacePath = dir }))
	return rewinds, repository, ledger, session.ID, dir
}
func controlPreviewDigest(t *testing.T, r *Rewinds, ctx context.Context, id, anchor string) string {
	t.Helper()
	preview, err := r.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview rewind", err)
	if len(preview.Issues) > 0 {
		t.Fatalf("preview issues: %+v", preview.Issues)
	}
	return preview.PlanDigest
}
