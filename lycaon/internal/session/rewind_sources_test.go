package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func appendRewindAsk(t *testing.T, mgr *Host, sessionID string) string {
	t.Helper()
	id := uuid.NewString()
	testutil.FailErr(t, "append ask", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(t.Context(), sessionID, api.Message{ID: id, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "change files"}))
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
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, id, first)
	testutil.FailErr(t, "preview suffix", err)
	if len(preview.Files) != 2 || len(preview.Issues) != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	p, err := rewindFixtureProject(t, mgr, ctx, id)
	testutil.FailErr(t, "project", err)
	ledger := mgr.ToolContext.SourceLedger.(*sourceledger.Store)
	fileIDs := map[string]string{}
	for _, name := range []string{"first.txt", "second.txt"} {
		head, err := ledger.History.ResolveHead(ctx, p.ID, p.BranchForRoot(p.Roots[0].ID), p.Roots[0].ID, name)
		testutil.FailErr(t, "read original head", err)
		fileIDs[name] = head.FileID
	}
	operationID := uuid.NewString()
	result, err := mgr.Chats.Rewinds.RewindToPrompt(ctx, operationID, id, first, preview.PlanDigest)
	testutil.FailErr(t, "rewind suffix", err)
	if result.TruncatedMessageCount != 2 || len(result.RestoredPaths) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	replay, err := mgr.Chats.Rewinds.RewindToPrompt(ctx, operationID, id, first, preview.PlanDigest)
	testutil.FailErr(t, "replay operation", err)
	if replay.TruncatedMessageCount != 2 {
		t.Fatalf("replay=%+v", replay)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		head, err := ledger.History.ResolveHeadByFile(ctx, p.ID, p.BranchForRoot(p.Roots[0].ID), fileIDs[name])
		testutil.FailErr(t, "read restored version", err)
		if head.State != "absent" {
			t.Fatalf("head=%+v", head)
		}
	}
	next := appendRewindAsk(t, mgr, id)
	summaries, err := ledger.Walk.WalkSummary(ctx, p.ID, id, []string{next})
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
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview before human edit", err)
	testutil.FailErr(t, "human edit without watcher", os.WriteFile(path, []byte("human work"), 0o644))
	_, err = mgr.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
	var blocked *sourceledger.RewindBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("rewind err=%v, want structured conflict", err)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read human work", err)
	if string(raw) != "human work" {
		t.Fatalf("human work lost: %q", raw)
	}
	messages, err := mgr.Runner.Transcript.GetMessages(ctx, id)
	testutil.FailErr(t, "read retained transcript", err)
	if len(messages) != 1 || messages[0].ID != anchor {
		t.Fatalf("transcript=%+v", messages)
	}
}

func TestSourceRewindRejectsStalePreviewAndContinuation(t *testing.T) {
	mgr, id, _ := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)
	anchor := appendRewindAsk(t, mgr, id)
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview", err)
	appendRewindAsk(t, mgr, id)
	_, err = mgr.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
	if !errors.Is(err, checkpointcontrol.ErrRewindPlanChanged) {
		t.Fatalf("stale preview err=%v", err)
	}
	continuation := uuid.NewString()
	testutil.FailErr(t, "append continuation", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, id, api.Message{ID: continuation, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Kind: api.MessageKindUserContinuation, Content: "more"}))
	_, err = mgr.Chats.Rewinds.PreviewRewind(ctx, id, continuation)
	if !errors.Is(err, checkpointcontrol.ErrRewindAnchorIneligible) {
		t.Fatalf("continuation preview err=%v", err)
	}
}

func TestSourceRewindChecksUnrecordedChangesAcrossTheWholeSuffix(t *testing.T) {
	mgr, id, root := newCheckpointTestSession(t)
	first := appendRewindAsk(t, mgr, id)
	second := appendRewindAsk(t, mgr, id)
	cp := sessioncheckpoint.New(mgr.Workspace.DataDir, root, mgr.Coordinator.Context.Sessions.(Store))
	_, err := cp.Open(t.Context(), id, second)
	testutil.FailErr(t, "open later checkpoint", err)
	testutil.FailErr(t, "capture unversioned path", cp.CapturePreImage(t.Context(), id, second, "blueprint.md"))
	testutil.FailErr(t, "simulate unversioned host mutation", os.WriteFile(filepath.Join(root, "blueprint.md"), []byte("host mutation"), 0o644))
	preview, err := mgr.Chats.Rewinds.PreviewRewind(t.Context(), id, first)
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
	cp := sessioncheckpoint.New(mgr.Workspace.DataDir, root, mgr.Coordinator.Context.Sessions.(Store))
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
	change, err := mgr.Coordinator.Tools.Projects.AttachRoot(ctx, p.ID, project.AttachRootParams{Path: secondary, Label: "secondary"})
	testutil.FailErr(t, "attach secondary root", err)
	anchor := appendRewindAsk(t, mgr, id)
	testutil.FailErr(t, "write primary file", os.WriteFile(filepath.Join(root, "same.txt"), []byte("primary"), 0o644))
	recordRewindTestEffect(t, mgr, id, "same.txt", nil, []byte("primary"), api.SourceChangeOpCreate)
	testutil.FailErr(t, "write secondary file", os.WriteFile(filepath.Join(secondary, "renamed.txt"), []byte("secondary"), 0o644))
	ledger := mgr.ToolContext.SourceLedger.(*sourceledger.Store)
	err = ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: change.Added.ID, FromRootID: change.Added.ID, FromPath: "same.txt", Path: "renamed.txt"}, ProjectID: p.ID, SessionID: id, Turn: 1, Origin: api.SourceChangeOriginAgent, Op: api.SourceChangeOpRename, Before: []byte("secondary"), After: []byte("secondary")})
	testutil.FailErr(t, "record secondary rename", err)
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, id, anchor)
	testutil.FailErr(t, "preview multiple roots", err)
	if len(preview.Files) != 2 || len(preview.Issues) != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	_, err = mgr.Chats.Rewinds.RewindToPrompt(ctx, uuid.NewString(), id, anchor, preview.PlanDigest)
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

type stubProjectWorkerLister struct {
	noopWorkerBranchClaim
	tasks []api.WorkerTask
}

func (*stubProjectWorkerLister) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

func (s *stubProjectWorkerLister) ListBySession(_ context.Context, projectID, sessionID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	all, err := s.List(context.Background(), projectID, status...)
	if err != nil {
		return nil, err
	}
	var out []api.WorkerTask
	for _, task := range all {
		if task.ParentSessionID == sessionID {
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *stubProjectWorkerLister) List(_ context.Context, projectID string, status ...api.WorkerStatus) ([]api.WorkerTask, error) {
	want := map[api.WorkerStatus]struct{}{}
	for _, st := range status {
		want[st] = struct{}{}
	}
	var out []api.WorkerTask
	for _, task := range s.tasks {
		if projectID != "" && task.ProjectID != projectID {
			continue
		}
		if len(want) > 0 {
			if _, ok := want[task.Status]; !ok {
				continue
			}
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *stubProjectWorkerLister) Get(jobID string) (*api.WorkerTask, bool) {
	for i := range s.tasks {
		if s.tasks[i].ID == jobID {
			return &s.tasks[i], true
		}
	}
	return nil, false
}

func TestRootDependentsInFlightWorkerAndBusySession(t *testing.T) {
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	dir := t.TempDir()
	p, err := reg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: dir, IsPrimary: ptrBool(true)}},
	})
	testutil.FailErr(t, "create project", err)
	rootID := p.Roots[0].ID

	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProjectRegistry(reg)
	mgr.SetWorkerQueue(&stubProjectWorkerLister{tasks: []api.WorkerTask{{
		ID:              "job-1",
		ParentSessionID: "sess-1",
		ProjectID:       p.ID,
		WorkspaceRootID: rootID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Status:          api.WorkerStatusRunning,
	}}})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	if err := store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = rootID
		s.Status = api.SessionStatusBusy
	}); err != nil {
		testutil.FailErr(t, "mark busy", err)
	}

	dep, err := mgr.ProjectControl.RootDependents(ctx, p.ID, rootID)
	testutil.FailErr(t, "projectcontrol.RootDependents", err)
	if len(dep.Workers) != 1 || dep.Workers[0].JobID != "job-1" {
		t.Fatalf("workers = %+v want job-1", dep.Workers)
	}
	if len(dep.Sessions) != 1 || dep.Sessions[0].SessionID != sess.ID {
		t.Fatalf("sessions = %+v want %s", dep.Sessions, sess.ID)
	}
	if !dep.HasAny() {
		t.Fatal("expected dependents")
	}
}

func TestRootDependentsIncludesBusySessionAcrossMultiRootUnion(t *testing.T) {
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir(), IsPrimary: ptrBool(true)}, {Path: t.TempDir()},
	}})
	testutil.FailErr(t, "create project", err)
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetProjectRegistry(reg)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspaceRootID = p.Roots[0].ID
		s.Status = api.SessionStatusBusy
	}))
	dependents, err := mgr.ProjectControl.RootDependents(ctx, p.ID, p.Roots[1].ID)
	testutil.FailErr(t, "root dependents", err)
	if len(dependents.Sessions) != 1 || dependents.Sessions[0].SessionID != sess.ID {
		t.Fatalf("sessions = %+v", dependents.Sessions)
	}
}

func ptrBool(v bool) *bool { return &v }

func TestListenLeaseCovers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		covered, asked []uint16
		want           bool
	}{
		{nil, []uint16{8000}, true},
		{nil, nil, true},
		{[]uint16{8000}, nil, false},
		{[]uint16{8000}, []uint16{8000}, true},
		{[]uint16{8000}, []uint16{9000}, false},
		{[]uint16{8000, 9000}, []uint16{8000, 9000}, true},
	}
	for _, tc := range cases {
		if got := portLeaseCovers(tc.covered, tc.asked); got != tc.want {
			t.Fatalf("covers(%v, %v)=%v want %v", tc.covered, tc.asked, got, tc.want)
		}
	}
}

func TestLoopbackRuntimeIsIndependentFromListenRuntime(t *testing.T) {
	t.Parallel()
	listen := approvalstate.NewSandboxPortGrantRuntime()
	connect := approvalstate.NewSandboxPortGrantRuntime()
	listen.GrantChat("chat", []uint16{8123}, "listen", "cp1", nil)
	if granted, _ := connect.SessionPorts("chat"); granted {
		t.Fatal("listener authority leaked into local client authority")
	}
	connect.GrantChat("chat", []uint16{8123}, "connect", "cp2", nil)
	granted, ports := connect.SessionPorts("chat")
	if !granted || len(ports) != 1 || ports[0] != 8123 {
		t.Fatalf("loopback lease: granted=%v ports=%v", granted, ports)
	}
	if _, ok := connect.RevokeByID("connect"); !ok {
		t.Fatal("loopback revoke failed")
	}
	if granted, _ := listen.SessionPorts("chat"); !granted {
		t.Fatal("connection revocation removed listener authority")
	}

	if begin, _ := listen.Begin("chat", "8123", "listen-call"); begin != approvalstate.SandboxAskMint {
		t.Fatalf("connection denial blocked listener review: %v", begin)
	}
}
