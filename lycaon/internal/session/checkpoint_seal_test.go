package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// checkpointPreImages is the engine's shared source content store, where a
// checkpoint seals each path's pre-turn bytes.
func checkpointPreImages(mgr *Manager) *sourceblob.Store {
	return sourceblob.New(filepath.Join(mgr.dataDir, enginepaths.SourceContentDirName))
}

func newCheckpointTestSession(t *testing.T) (*Manager, string, string) {
	t.Helper()
	database := testdbfixture.Open(t, "session.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	st := sessionstore.NewSQL(database)
	mgr := newTestManagerWithStore(t, st)
	mgr.SetProjectRegistry(project.NewSQLRegistry(database))
	ledger := sourceledger.New(database, filepath.Join(mgr.dataDir, "source-content"))
	mgr.SetSourceLedger(ledger)
	mutations := project.NewSourceMutationService(database, ledger)
	mgr.SetSourceMutations(mutations)
	mgr.Rewinds.SetSourceRewinds(&sourcerewind.Service{Ledger: ledger, Mutations: mutations})
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "bind workspace path", st.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspacePath = dir
	}))
	return mgr, sess.ID, dir
}

func visibleUserMessageIDs(t *testing.T, mgr *Manager, sessionID string) []string {
	t.Helper()
	msgs, err := mgr.Transcript.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "get messages", err)
	var out []string
	for _, m := range msgs {
		if m.Role == api.MessageRoleUser && m.Visibility != api.MessageVisibilityInternal {
			out = append(out, m.ID)
		}
	}
	return out
}

// The visible user message identifies its pre-turn checkpoint.
func TestPromptOpensCheckpointForTheVisibleUserMessage(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "hello")
	testutil.FailErr(t, "prompt", err)

	anchors := visibleUserMessageIDs(t, mgr, sessionID)
	if len(anchors) != 1 {
		t.Fatalf("visible user messages = %d, want 1", len(anchors))
	}
	store := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	man, err := store.Load(t.Context(), sessionID, anchors[0])
	testutil.FailErr(t, "load checkpoint for the anchor", err)
	if man.AnchorMessageID != anchors[0] {
		t.Fatalf("anchor = %q, want %q", man.AnchorMessageID, anchors[0])
	}
	if len(man.Paths) != 0 {
		t.Fatalf("a freshly opened checkpoint holds no paths yet, got %v", man.Paths)
	}
}

func TestPrimaryMutationCapturesPreTurnBytesIntoTheOpenAnchor(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("before"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "hello")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	// The mutation hook runs before the disk write.
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "simulate the write", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("after"), 0o644))

	store := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	man, err := store.Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load checkpoint", err)
	entry, ok := man.Paths["foo.go"]
	if !ok {
		t.Fatalf("checkpoint missing the mutated path; paths = %v", man.Paths)
	}
	blob, err := checkpointPreImages(mgr).GetSHA(entry.SHA256)
	testutil.FailErr(t, "read blob", err)
	if string(blob) != "before" {
		t.Fatalf("blob = %q, want the pre-turn bytes", blob)
	}
}

// Each prompt retains its own pre-turn bytes.
func TestEachPromptOpensItsOwnAnchorAndOlderAnchorsKeepTheirBytes(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	path := filepath.Join(dir, "foo.go")
	testutil.FailErr(t, "seed v1", os.WriteFile(path, []byte("v1"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "first ask")
	testutil.FailErr(t, "first prompt", err)
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "write v2", os.WriteFile(path, []byte("v2"), 0o644))

	_, err = mgr.Submissions.Prompt(ctx, sessionID, "second ask")
	testutil.FailErr(t, "second prompt", err)
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "write v3", os.WriteFile(path, []byte("v3"), 0o644))

	anchors := visibleUserMessageIDs(t, mgr, sessionID)
	if len(anchors) != 2 {
		t.Fatalf("anchors = %d, want 2", len(anchors))
	}
	store := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store)
	for i, want := range []string{"v1", "v2"} {
		man, err := store.Load(t.Context(), sessionID, anchors[i])
		testutil.FailErr(t, "load anchor", err)
		entry, ok := man.Paths["foo.go"]
		if !ok {
			t.Fatalf("anchor %d missing foo.go", i)
		}
		blob, err := checkpointPreImages(mgr).GetSHA(entry.SHA256)
		testutil.FailErr(t, "read blob", err)
		if string(blob) != want {
			t.Fatalf("anchor %d blob = %q, want %q", i, blob, want)
		}
	}
}

// Ledger overflow marks the checkpoint incomplete.

func TestUnderCapTurnLeavesTheCheckpointComplete(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("before"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "touch one file")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]
	mgr.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")

	man, err := sessioncheckpoint.New(mgr.dataDir, dir, mgr.store).Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load manifest", err)
	if man.Truncated {
		t.Fatal("an ordinary turn must not be reported as truncated")
	}
}

func checkpointCaller(t *testing.T, mgr *Manager) context.Context {
	t.Helper()
	owner, err := mgr.store.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	return people.WithCaller(t.Context(), owner)
}

func rewindTest(t *testing.T, mgr *Manager, ctx context.Context, operationID, sessionID, anchor string) (*checkpointcontrol.RewindResult, error) {
	t.Helper()
	if _, ok := people.Caller(ctx); !ok {
		owner, err := mgr.store.HostOwner(ctx)
		testutil.FailErr(t, "host owner", err)
		ctx = people.WithCaller(ctx, owner)
	}
	preview, err := mgr.Rewinds.PreviewRewind(ctx, sessionID, anchor)
	digest := ""
	if err == nil {
		digest = preview.PlanDigest
	}
	return mgr.Rewinds.RewindToPrompt(ctx, operationID, sessionID, anchor, digest)
}

func recordRewindTestEffect(t *testing.T, mgr *Manager, sessionID, path string, before, after []byte, op api.SourceChangeOp) {
	t.Helper()
	ctx := t.Context()
	p, err := rewindFixtureProject(t, mgr, ctx, sessionID)
	testutil.FailErr(t, "resolve project", err)
	turn, err := mgr.store.UserTurnOrdinal(ctx, sessionID)
	testutil.FailErr(t, "resolve turn", err)
	testutil.FailErr(t, "record source effect", mgr.sourceLedger.Record(ctx, sourceledger.RecordInput{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: path, Op: op,
		Origin: api.SourceChangeOriginAgent, SessionID: sessionID, Turn: turn, Before: before, After: after}))
}

func rewindFixtureProject(t *testing.T, mgr *Manager, ctx context.Context, id string) (*project.Project, error) {
	t.Helper()
	sess, err := mgr.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return mgr.projects.Get(ctx, sess.ProjectID)
}

func rewindFixtureHasMessage(messages []api.Message, id string) (api.Message, bool) {
	for _, message := range messages {
		if message.ID == id {
			return message, true
		}
	}
	return api.Message{}, false
}
