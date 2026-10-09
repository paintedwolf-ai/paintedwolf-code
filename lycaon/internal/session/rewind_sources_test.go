package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func appendRewindAsk(t *testing.T, mgr *Manager, sessionID string) string {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "append ask", mgr.store.AppendMessages(t.Context(), sessionID, api.Message{ID: id, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "change files"}))
	return id
}

func TestSourceRewindRestoresEntireSuffixAndRecordsUserVersions(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)
	first := appendRewindAsk(t, mgr, id)
	testutil.FailErr(t, "create first file", os.WriteFile(filepath.Join(root, "first.txt"), []byte("first"), 0o644))
	recordRewindTestEffect(t, mgr, id, "first.txt", nil, []byte("first"), api.SourceChangeOpCreate)
	appendRewindAsk(t, mgr, id)
	testutil.FailErr(t, "create second file", os.WriteFile(filepath.Join(root, "second.txt"), []byte("second"), 0o644))
	recordRewindTestEffect(t, mgr, id, "second.txt", nil, []byte("second"), api.SourceChangeOpCreate)
	preview, err := mgr.Rewinds.PreviewRewind(ctx, id, first)
	testutil.FailErr(t, "preview suffix", err)
	if len(preview.Files) != 2 || len(preview.Issues) != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	p, err := rewindFixtureProject(t, mgr, ctx, id)
	testutil.FailErr(t, "project", err)
	ledger := mgr.sourceLedger.(*sourceledger.Store)
	fileIDs := map[string]string{}
	for _, name := range []string{"first.txt", "second.txt"} {
		head, err := ledger.ResolveHead(ctx, p.ID, p.BranchForRoot(p.Roots[0].ID), p.Roots[0].ID, name)
		testutil.FailErr(t, "read original head", err)
		fileIDs[name] = head.FileID
	}
	operationID := uuid.NewString()
	result, err := mgr.Rewinds.RewindToPrompt(ctx, operationID, id, first, preview.PlanDigest)
	testutil.FailErr(t, "rewind suffix", err)
	if result.TruncatedMessageCount != 2 || len(result.RestoredPaths) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	replay, err := mgr.Rewinds.RewindToPrompt(ctx, operationID, id, first, preview.PlanDigest)
	testutil.FailErr(t, "replay operation", err)
	if replay.TruncatedMessageCount != 2 {
		t.Fatalf("replay=%+v", replay)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		head, err := ledger.ResolveHeadByFile(ctx, p.ID, p.BranchForRoot(p.Roots[0].ID), fileIDs[name])
		testutil.FailErr(t, "read restored version", err)
		if head.State != "absent" {
			t.Fatalf("head=%+v", head)
		}
	}
	next := appendRewindAsk(t, mgr, id)
	summaries, err := ledger.WalkSummary(ctx, p.ID, id, []string{next})
	testutil.FailErr(t, "read next turn changes", err)
	if len(summaries) != 1 || summaries[0].Steps != 0 {
		t.Fatalf("discarded changes attributed to new turn: %+v", summaries)
	}
}

func TestSourceRewindPreservesUnobservedHumanEditAndTranscript(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)
	anchor := appendRewindAsk(t, mgr, id)
	path := filepath.Join(root, "file.txt")
	testutil.FailErr(t, "write agent file", os.WriteFile(path, []byte("agent"), 0o644))
	recordRewindTestEffect(t, mgr, id, "file.txt", nil, []byte("agent"), api.SourceChangeOpCreate)
	preview, err := mgr.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview before human edit", err)
	testutil.FailErr(t, "human edit without watcher", os.WriteFile(path, []byte("human work"), 0o644))
	_, err = mgr.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
	var blocked *sourceledger.RewindBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("rewind err=%v, want structured conflict", err)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read human work", err)
	if string(raw) != "human work" {
		t.Fatalf("human work lost: %q", raw)
	}
	messages, err := mgr.Transcript.GetMessages(ctx, id)
	testutil.FailErr(t, "read retained transcript", err)
	if len(messages) != 1 || messages[0].ID != anchor {
		t.Fatalf("transcript=%+v", messages)
	}
}

func TestSourceRewindRejectsStalePreviewAndContinuation(t *testing.T) {
	mgr, id, _ := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)
	anchor := appendRewindAsk(t, mgr, id)
	preview, err := mgr.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview", err)
	appendRewindAsk(t, mgr, id)
	_, err = mgr.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
	if !errors.Is(err, checkpointcontrol.ErrRewindPlanChanged) {
		t.Fatalf("stale preview err=%v", err)
	}
	continuation := uuid.NewString()
	testutil.FailErr(t, "append continuation", mgr.store.AppendMessages(ctx, id, api.Message{ID: continuation, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Kind: api.MessageKindUserContinuation, Content: "more"}))
	_, err = mgr.Rewinds.PreviewRewind(ctx, id, continuation)
	if !errors.Is(err, checkpointcontrol.ErrRewindAnchorIneligible) {
		t.Fatalf("continuation preview err=%v", err)
	}
}

func TestSourceRewindChecksUnrecordedChangesAcrossTheWholeSuffix(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	first := appendRewindAsk(t, mgr, id)
	second := appendRewindAsk(t, mgr, id)
	cp := sessioncheckpoint.New(mgr.dataDir, root, mgr.store)
	_, err := cp.Open(t.Context(), id, second)
	testutil.FailErr(t, "open later checkpoint", err)
	testutil.FailErr(t, "capture unversioned path", cp.CapturePreImage(t.Context(), id, second, "blueprint.md"))
	testutil.FailErr(t, "simulate unversioned host mutation", os.WriteFile(filepath.Join(root, "blueprint.md"), []byte("host mutation"), 0o644))
	preview, err := mgr.Rewinds.PreviewRewind(t.Context(), id, first)
	testutil.FailErr(t, "preview incomplete suffix", err)
	if len(preview.Issues) != 1 || preview.Issues[0].Code != "unrecorded_change" || preview.PlanDigest != "" {
		t.Fatalf("preview=%+v", preview)
	}
}

func TestSourceRewindRestoresDeletedExecutableMode(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	anchor := appendRewindAsk(t, mgr, id)
	path := filepath.Join(root, "run.sh")
	testutil.FailErr(t, "create executable", os.WriteFile(path, []byte("exit 0\n"), 0o751))
	cp := sessioncheckpoint.New(mgr.dataDir, root, mgr.store)
	_, err := cp.Open(t.Context(), id, anchor)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture executable", cp.CapturePreImage(t.Context(), id, anchor, "run.sh"))
	testutil.FailErr(t, "remove executable", os.Remove(path))
	recordRewindTestEffect(t, mgr, id, "run.sh", []byte("exit 0\n"), nil, api.SourceChangeOpDelete)
	_, err = rewindTest(t, mgr, t.Context(), uuid.NewString(), id, anchor)
	testutil.FailErr(t, "rewind deletion", err)
	info, err := os.Stat(path)
	testutil.FailErr(t, "read restored executable", err)
	if info.Mode().Perm() != 0o751 {
		t.Fatalf("mode=%v", info.Mode())
	}
}

func TestSourceRewindResolvesEachRootAndRenamedFile(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)
	p, err := rewindFixtureProject(t, mgr, ctx, id)
	testutil.FailErr(t, "resolve project", err)
	secondary := t.TempDir()
	change, err := mgr.projects.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: secondary, Label: "secondary"})
	testutil.FailErr(t, "attach secondary root", err)
	anchor := appendRewindAsk(t, mgr, id)
	testutil.FailErr(t, "write primary file", os.WriteFile(filepath.Join(root, "same.txt"), []byte("primary"), 0o644))
	recordRewindTestEffect(t, mgr, id, "same.txt", nil, []byte("primary"), api.SourceChangeOpCreate)
	testutil.FailErr(t, "write secondary file", os.WriteFile(filepath.Join(secondary, "renamed.txt"), []byte("secondary"), 0o644))
	ledger := mgr.sourceLedger.(*sourceledger.Store)
	err = ledger.Record(ctx, sourceledger.RecordInput{ProjectID: p.ID, RootID: change.Added.ID, FromRootID: change.Added.ID, FromPath: "same.txt", Path: "renamed.txt", SessionID: id, Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpRename, Before: []byte("secondary"), After: []byte("secondary")})
	testutil.FailErr(t, "record secondary rename", err)
	preview, err := mgr.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview multiple roots", err)
	if len(preview.Files) != 2 || len(preview.Issues) != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	_, err = mgr.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
	testutil.FailErr(t, "rewind multiple roots", err)
	if _, err := os.Stat(filepath.Join(root, "same.txt")); !os.IsNotExist(err) {
		t.Fatalf("primary file remains: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(secondary, "same.txt"))
	testutil.FailErr(t, "read original secondary name", err)
	if string(raw) != "secondary" {
		t.Fatalf("secondary=%q", raw)
	}
	if _, err := os.Stat(filepath.Join(secondary, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("renamed file remains: %v", err)
	}
}
