package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

// revisionFixture drives native captures and pinned writes through the real
// document owner, write door, source ledger and program receipt store.
type revisionFixture struct {
	service   *editordoc.Service
	adapter   editorDocumentsAdapter
	project   *project.Project
	rootID    string
	root      string
	tctx      tools.ToolContext
	revisions native.ExecutionRevisions
	write     *native.WriteTool
	recorder  *invocation.SQLRecorder
	session   string
	execution string
	program   string
}

func newRevisionFixture(t *testing.T, files map[string]string) *revisionFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID, session := testdbseed.DefaultProjectID, uuid.NewString(), "program-session"
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testdbseed.InsertSession(t, sqlDB, session, projectID)
	for name, content := range files {
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	ledger := sourceledger.New(sqlDB, "")
	service := editordoc.New(editordoc.NewStore(sqlDB), ledger, ledger.History, adapterRoots{p: p})
	adapter := editorDocumentsAdapter{service: service}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "coordinator", Tools: map[string]bool{"read": true, "write": true}}})
	f := &revisionFixture{service: service, adapter: adapter, project: p, rootID: rootID, root: root, revisions: native.ExecutionRevisions{Boundary: boundary},
		write: &native.WriteTool{Boundary: boundary}, recorder: invocation.NewSQLRecorder(sqlDB), session: session}
	f.tctx = tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: projectID, SessionID: session, Agent: "coordinator", ToolCallID: "model-command-call"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: rootID, Label: "root", Path: root, IsPrimary: true}}, ActiveRootID: rootID,
			SourceWorkspaceKind: api.SourceWorkspaceKindProject, EditorDocuments: adapter, SourceLedger: ledger, RepoFileCount: len(files), RepoFileCountKnown: true},
	}
	f.beginExecution(t)
	return f
}

// beginExecution attaches one dynamic write execution under an actual model call.
func (f *revisionFixture) beginExecution(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	contract, _ := toolcontract.Lookup("command")
	parent, err := f.recorder.Begin(ctx, invocation.Start{ProjectID: f.project.ID, SessionID: f.session, ToolCallID: "model-command-call", ToolName: "command", Args: map[string]any{"command": "transform"}, Contract: contract})
	testutil.FailErr(t, "begin model call", err)
	source := "transform"
	digest := sha256.Sum256([]byte(source))
	sourceDigest := hex.EncodeToString(digest[:])
	f.program = uuid.NewString()
	rootExecution := uuid.NewString()
	span := toolcontract.SourceSpan{EndByte: len(source), Line: 1, Column: 1}
	testutil.FailErr(t, "begin program", f.recorder.BeginProgram(ctx, invocation.ProgramStart{Version: invocation.ProgramVersion{ProgramID: f.program, ParentReceiptID: parent.ID, RootExecutionID: rootExecution,
		LanguageVersion: toolcontract.LanguageVersion, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", SourceDigest: sourceDigest, SourceReference: "output:program"},
		PlanReference: "output:plan", SourceExact: true, SyntaxValidated: true,
		Sources: []invocation.ProgramSource{{UnitID: "submitted", Pin: toolcontract.SourcePin{SourceID: "submitted", Revision: sourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: sourceDigest, EndByte: int64(len(source))}, DetailReference: "output:program"}},
		Nodes:   []invocation.ProgramNode{{ID: "write-node", SourceUnitID: "submitted", Span: span}}}))
	write, _ := toolcontract.Lookup("write")
	definition, err := toolcontract.ToolOperation("write", json.RawMessage(`{"type":"object"}`), write)
	testutil.FailErr(t, "freeze write definition", err)
	f.execution = uuid.NewString()
	in := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: f.program, NodeID: "write-node", ExecutionID: f.execution, ParentExecutionID: rootExecution},
		ActorID: "fixture-person", SessionID: f.session, Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "submitted", Span: span, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	expanded, release, err := toolcontract.NewReviewedOperationInputs(ctx, reviewMemory{}, map[string]any{"path": "out"})
	testutil.FailErr(t, "freeze expanded inputs", err)
	defer release()
	testutil.FailErr(t, "prepare child", f.recorder.PrepareChild(ctx, invocation.ChildStart{ParentReceiptID: parent.ID, Invocation: in, Definition: definition, ArgumentsReference: "output:arguments", ExpandedInputs: expanded, Canonical: "transform > out"}))
}

type reviewMemory struct{}

func (reviewMemory) ReserveProgramState(context.Context, int64) (func(), error) { return func() {}, nil }

func (f *revisionFixture) disk(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, name))
	testutil.FailErr(t, "read "+name, err)
	return string(raw)
}

func (f *revisionFixture) absent(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(f.root, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists: %v", name, err)
	}
}

// personTypes leaves an unsaved draft in the shared document, as a window would.
func (f *revisionFixture) personTypes(t *testing.T, ctx context.Context, name, content string) *editordoc.Document {
	t.Helper()
	opened, err := f.service.Open(ctx, f.project, name, f.rootID, "", "window", nil)
	testutil.FailErr(t, "open "+name, err)
	typed, err := f.service.ReplaceSnapshot(ctx, opened.ID, f.project.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: opened.Revision}, Content: content, EOL: "lf"})
	testutil.FailErr(t, "type into "+name, err)
	return typed
}

func (f *revisionFixture) noReadBases(t *testing.T) {
	t.Helper()
	if reads := f.adapter.FreezeAgentReads(f.project.ID, f.session); len(reads) != 0 {
		t.Fatalf("program execution seeded conversational read bases: %+v", reads)
	}
}

func (f *revisionFixture) pinnedWrite(path, content string, destination toolcontract.ExecutionRevision, sources ...toolcontract.ExecutionRevision) native.WriteInput {
	return native.WriteInput{Path: path, Content: content, Admission: native.WriteExecutionPinned, Destination: destination, Sources: sources}
}

func requireRevisionConflict(t *testing.T, err error, role toolcontract.ExecutionRevisionRole) *native.ExecutionRevisionConflict {
	t.Helper()
	var conflict *native.ExecutionRevisionConflict
	reject := toolrejection.AsToolReject(err)
	if !errors.As(err, &conflict) || reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" || conflict.Pinned.Role != role {
		t.Fatalf("want %s revision conflict, got %v", role, err)
	}
	return conflict
}

func bumpVersion(t *testing.T, transforms *int, content string) string {
	t.Helper()
	*transforms++
	var value map[string]any
	testutil.FailErr(t, "decode json", json.Unmarshal([]byte(content), &value))
	value["version"] = 2
	encoded, err := json.Marshal(value)
	testutil.FailErr(t, "encode json", err)
	return string(encoded) + "\n"
}

func TestCommandTransformsUnseenFileThroughExecutionPins(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"config.json": `{"name":"app","version":1}` + "\n"})
	ctx := t.Context()
	source, err := f.revisions.CaptureSource(ctx, f.tctx, "config.json")
	testutil.FailErr(t, "capture unseen source", err)
	if source.Revision.Mode != toolcontract.AcceptedDocumentRevision || source.Revision.DocumentRevision <= 0 || source.Revision.DiskSHA256 != textfile.SHA256([]byte(f.disk(t, "config.json"))) || source.Content != f.disk(t, "config.json") {
		t.Fatalf("source = %+v", source)
	}
	f.noReadBases(t)
	transforms := 0
	output := bumpVersion(t, &transforms, source.Content)
	destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "config.out.json")
	testutil.FailErr(t, "capture absent destination", err)
	if destination.Mode != toolcontract.AbsentRevision {
		t.Fatalf("destination = %+v", destination)
	}
	pins := []toolcontract.ExecutionRevision{source.Revision, destination}
	testutil.FailErr(t, "record pins before preparation", f.recorder.RecordRevisionPins(ctx, f.execution, pins))
	reviews := 0
	f.tctx.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		reviews++
		if len(changes) != 1 || changes[0].Preview.After != output {
			return errors.New("review saw different prepared bytes")
		}
		return nil
	}
	result, err := f.write.Apply(ctx, f.pinnedWrite("config.out.json", output, destination, source.Revision), f.tctx)
	testutil.FailErr(t, "publish transform", err)
	if !result.Saved || reviews != 1 || transforms != 1 || f.disk(t, "config.out.json") != output {
		t.Fatalf("result=%+v reviews=%d disk=%q", result, reviews, f.disk(t, "config.out.json"))
	}
	if accepted, published := result.SettlementRevisions(); published != "sha256:"+textfile.SHA256([]byte(output)) || accepted != published {
		t.Fatalf("settlement revisions = %q %q", accepted, published)
	}
	f.noReadBases(t)
	recorded, err := f.recorder.RevisionPins(ctx, f.session, f.execution)
	testutil.FailErr(t, "read receipt pins", err)
	if !slices.Equal(recorded, pins) {
		t.Fatalf("receipt pins = %+v", recorded)
	}
	later, err := f.revisions.CaptureSource(ctx, f.tctx, "config.out.json")
	testutil.FailErr(t, "later statement captures published write", err)
	if later.Content != output || later.Revision.DiskSHA256 != textfile.SHA256([]byte(output)) {
		t.Fatalf("later capture = %+v", later)
	}
	f.noReadBases(t)
}

func TestCommandTransformConflictsWhenDestinationMovesDuringReview(t *testing.T) {
	cases := map[string]func(t *testing.T, ctx context.Context, f *revisionFixture){
		"person types": func(t *testing.T, ctx context.Context, f *revisionFixture) {
			f.personTypes(t, ctx, "a.json", `{"human":true}`+"\n")
		},
		"external process writes": func(t *testing.T, _ context.Context, f *revisionFixture) {
			testutil.FailErr(t, "external write", os.WriteFile(filepath.Join(f.root, "a.json"), []byte(`{"external":true}`+"\n"), 0o644))
		},
	}
	for name, move := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRevisionFixture(t, map[string]string{"input.json": `{"version":1}` + "\n", "a.json": `{"old":true}` + "\n"})
			ctx := t.Context()
			source, err := f.revisions.CaptureSource(ctx, f.tctx, "input.json")
			testutil.FailErr(t, "capture source", err)
			destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "a.json")
			testutil.FailErr(t, "capture destination", err)
			transforms, reviews := 0, 0
			output := bumpVersion(t, &transforms, source.Content)
			f.tctx.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
				reviews++
				move(t, ctx, f)
				return nil
			}
			_, err = f.write.Apply(ctx, f.pinnedWrite("a.json", output, destination, source.Revision), f.tctx)
			conflict := requireRevisionConflict(t, err, toolcontract.DestinationRevision)
			if !conflict.Pinned.Same(destination) || reviews != 1 || transforms != 1 {
				t.Fatalf("conflict=%+v reviews=%d transforms=%d", conflict, reviews, transforms)
			}
			want := `{"old":true}` + "\n"
			if name == "external process writes" {
				want = `{"external":true}` + "\n"
			}
			if raw := f.disk(t, "a.json"); raw != want {
				t.Fatalf("disk = %q, want %q", raw, want)
			}
			doc, ok, err := f.adapter.OpenDocument(ctx, f.project.ID, f.project.SourceBranch, f.rootID, "a.json")
			testutil.FailErr(t, "inspect document", err)
			if !ok || doc.Text == output || strings.Contains(doc.Text, "version") {
				t.Fatalf("stale transform landed in the document: %+v", doc)
			}
			f.noReadBases(t)
		})
	}
}

func TestCommandCapturesAcceptedDraftWhileExternalLaunchDeclaresDisk(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"a.txt": "base\n", "b.txt": "other\n"})
	ctx := t.Context()
	f.personTypes(t, ctx, "a.txt", "unsaved draft\n")
	source, err := f.revisions.CaptureSource(ctx, f.tctx, "a.txt")
	testutil.FailErr(t, "capture accepted draft", err)
	declared, err := f.revisions.DeclareLaunchInput(ctx, f.tctx, "a.txt")
	testutil.FailErr(t, "declare launch input", err)
	diskSHA := textfile.SHA256([]byte("base\n"))
	if source.Content != "unsaved draft\n" || source.Revision.Mode != toolcontract.AcceptedDocumentRevision || source.Revision.DiskSHA256 != diskSHA || source.Revision.ContentSHA256 != textfile.SHA256([]byte("unsaved draft\n")) {
		t.Fatalf("native capture = %+v", source)
	}
	if declared.Mode != toolcontract.PublishedDiskRevision || declared.Basis != toolcontract.DeclaredAtLaunch || declared.DiskSHA256 != diskSHA || declared.ContentSHA256 != diskSHA || declared.ContentBytes != 5 {
		t.Fatalf("launch declaration = %+v", declared)
	}
	destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "b.txt")
	testutil.FailErr(t, "capture destination", err)
	result, err := f.write.Apply(ctx, f.pinnedWrite("b.txt", strings.ToUpper(source.Content), destination, source.Revision), f.tctx)
	testutil.FailErr(t, "publish from draft", err)
	if !result.Saved || f.disk(t, "b.txt") != "UNSAVED DRAFT\n" {
		t.Fatalf("result = %+v", result)
	}
	testutil.FailErr(t, "record native and launch provenance", f.recorder.RecordRevisionPins(ctx, f.execution, []toolcontract.ExecutionRevision{source.Revision, destination, declared}))
	if f.disk(t, "a.txt") != "base\n" {
		t.Fatal("unrelated draft was saved")
	}
	dirty, err := f.adapter.DirtyDocuments(ctx, f.project.ID, f.project.SourceBranch)
	testutil.FailErr(t, "dirty documents", err)
	if len(dirty) != 1 || dirty[0].Path != "a.txt" || dirty[0].Text != "unsaved draft\n" {
		t.Fatalf("dirty = %+v", dirty)
	}
	f.noReadBases(t)
}

func TestCommandPinsConflictOnDeleteRenameAndCreateRaces(t *testing.T) {
	type race struct {
		source, destination string
		role                toolcontract.ExecutionRevisionRole
		move                func(t *testing.T, f *revisionFixture)
		after               func(t *testing.T, f *revisionFixture)
	}
	cases := map[string]race{
		"source deleted": {source: "input.txt", destination: "out.txt", role: toolcontract.SourceRevision,
			move:  func(t *testing.T, f *revisionFixture) { testutil.FailErr(t, "delete", os.Remove(filepath.Join(f.root, "input.txt"))) },
			after: func(t *testing.T, f *revisionFixture) { f.absent(t, "out.txt") }},
		"source renamed": {source: "input.txt", destination: "out.txt", role: toolcontract.SourceRevision,
			move: func(t *testing.T, f *revisionFixture) {
				testutil.FailErr(t, "rename", os.Rename(filepath.Join(f.root, "input.txt"), filepath.Join(f.root, "moved.txt")))
			},
			after: func(t *testing.T, f *revisionFixture) { f.absent(t, "out.txt") }},
		"destination replaced by rename": {source: "input.txt", destination: "existing.txt", role: toolcontract.DestinationRevision,
			move: func(t *testing.T, f *revisionFixture) {
				testutil.FailErr(t, "rename over", os.Rename(filepath.Join(f.root, "other.txt"), filepath.Join(f.root, "existing.txt")))
			},
			after: func(t *testing.T, f *revisionFixture) {
				if f.disk(t, "existing.txt") != "other\n" {
					t.Fatal("renamed-in file overwritten")
				}
			}},
		"destination deleted": {source: "input.txt", destination: "existing.txt", role: toolcontract.DestinationRevision,
			move: func(t *testing.T, f *revisionFixture) {
				testutil.FailErr(t, "delete", os.Remove(filepath.Join(f.root, "existing.txt")))
			},
			after: func(t *testing.T, f *revisionFixture) { f.absent(t, "existing.txt") }},
		"destination created": {source: "input.txt", destination: "new.txt", role: toolcontract.DestinationRevision,
			move: func(t *testing.T, f *revisionFixture) {
				testutil.FailErr(t, "create", os.WriteFile(filepath.Join(f.root, "new.txt"), []byte("theirs\n"), 0o644))
			},
			after: func(t *testing.T, f *revisionFixture) {
				if f.disk(t, "new.txt") != "theirs\n" {
					t.Fatal("concurrent create overwritten")
				}
			}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRevisionFixture(t, map[string]string{"input.txt": "input\n", "existing.txt": "existing\n", "other.txt": "other\n"})
			ctx := t.Context()
			source, err := f.revisions.CaptureSource(ctx, f.tctx, tc.source)
			testutil.FailErr(t, "capture source", err)
			destination, err := f.revisions.CaptureDestination(ctx, f.tctx, tc.destination)
			testutil.FailErr(t, "capture destination", err)
			tc.move(t, f)
			_, err = f.write.Apply(ctx, f.pinnedWrite(tc.destination, "transformed\n", destination, source.Revision), f.tctx)
			requireRevisionConflict(t, err, tc.role)
			tc.after(t, f)
			f.noReadBases(t)
		})
	}
}

func TestCommandAliasedTransformPublishesOnceAndNeverReplays(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"a.json": `{"version":1}` + "\n"})
	ctx := t.Context()
	source, err := f.revisions.CaptureSource(ctx, f.tctx, "a.json")
	testutil.FailErr(t, "capture source", err)
	destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "a.json")
	testutil.FailErr(t, "capture aliased destination", err)
	if !source.Revision.Same(destination) {
		t.Fatalf("aliased captures differ: %+v %+v", source.Revision, destination)
	}
	transforms := 0
	output := bumpVersion(t, &transforms, source.Content)
	input := f.pinnedWrite("a.json", output, destination, source.Revision)
	result, err := f.write.Apply(ctx, input, f.tctx)
	testutil.FailErr(t, "publish in place", err)
	accepted, published := result.SettlementRevisions()
	if !result.Saved || result.DocumentID != destination.DocumentID || result.AcceptedRevision != destination.DocumentRevision+1 ||
		accepted != "document:"+destination.DocumentID+"@"+strconv.FormatInt(destination.DocumentRevision+1, 10) || published != "sha256:"+textfile.SHA256([]byte(output)) {
		t.Fatalf("result=%+v accepted=%q published=%q", result, accepted, published)
	}
	_, err = f.write.Apply(ctx, input, f.tctx)
	requireRevisionConflict(t, err, toolcontract.DestinationRevision)
	if f.disk(t, "a.json") != output || transforms != 1 {
		t.Fatal("replayed stale transform")
	}
	next, err := f.revisions.CaptureSource(ctx, f.tctx, "a.json")
	testutil.FailErr(t, "later statement capture", err)
	if next.Content != output || next.Revision.DocumentRevision <= destination.DocumentRevision {
		t.Fatalf("later statement did not see published write: %+v", next)
	}
	f.noReadBases(t)
}

func TestCommandPublicationBarrierBlocksDependentLaunch(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"dir/a.txt": "base\n", "b.txt": "saved\n"})
	ctx := t.Context()
	f.personTypes(t, ctx, "b.txt", "unrelated draft\n")
	source, err := f.revisions.CaptureSource(ctx, f.tctx, "dir/a.txt")
	testutil.FailErr(t, "capture source", err)
	destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "dir/a.txt")
	testutil.FailErr(t, "capture destination", err)
	dir := filepath.Join(f.root, "dir")
	testutil.FailErr(t, "lock directory", os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	result, err := f.write.Apply(ctx, f.pinnedWrite("dir/a.txt", "accepted\n", destination, source.Revision), f.tctx)
	testutil.FailErr(t, "accept pinned write", err)
	if !result.Accepted || result.Saved || result.PublicationError == nil || result.HeldVersionID == "" {
		t.Fatalf("result = %+v", result)
	}
	if _, published := result.SettlementRevisions(); published != "" {
		t.Fatalf("unpublished write reported %q", published)
	}
	var barrier native.PublicationBarrier
	testutil.FailErr(t, "track accepted write", barrier.Track(result))
	launched := false
	launch := func() error {
		if err := barrier.AdmitDependentLaunch(ctx, f.tctx); err != nil {
			return err
		}
		launched = true
		return nil
	}
	err = launch()
	var pending *native.PublicationPending
	reject := toolrejection.AsToolReject(err)
	if !errors.As(err, &pending) || reject == nil || reject.Code != "COMMAND_PUBLICATION_PENDING" || launched || !slices.Equal(pending.Paths, []string{"dir/a.txt"}) {
		t.Fatalf("launch admitted: launched=%v err=%v", launched, err)
	}
	if f.disk(t, "dir/a.txt") != "base\n" || f.disk(t, "b.txt") != "saved\n" {
		t.Fatal("barrier saved a draft")
	}
	held, ok, err := f.adapter.OpenDocument(ctx, f.project.ID, f.project.SourceBranch, f.rootID, "dir/a.txt")
	testutil.FailErr(t, "inspect accepted state", err)
	if !ok || held.Text != "accepted\n" || !held.Dirty {
		t.Fatalf("accepted state not recoverable: %+v", held)
	}
	testutil.FailErr(t, "unlock directory", os.Chmod(dir, 0o755))
	_, err = f.service.Save(ctx, f.project, held.ID, "window", uuid.NewString(), "", 0, held.Revision)
	testutil.FailErr(t, "person saves accepted state", err)
	testutil.FailErr(t, "dependent launch after publication", launch())
	if !launched || f.disk(t, "dir/a.txt") != "accepted\n" {
		t.Fatal("published write did not release the barrier")
	}
	if f.disk(t, "b.txt") != "saved\n" {
		t.Fatal("unrelated draft was saved")
	}
	f.noReadBases(t)
}

func TestCommandInterruptedPinnedWriteLeavesNoPartialFile(t *testing.T) {
	f := newRevisionFixture(t, map[string]string{"a.txt": "base\n"})
	f.tctx.Source.EditorDocuments = nil
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source, err := f.revisions.CaptureSource(ctx, f.tctx, "a.txt")
	testutil.FailErr(t, "capture disk source", err)
	destination, err := f.revisions.CaptureDestination(ctx, f.tctx, "a.txt")
	testutil.FailErr(t, "capture disk destination", err)
	if source.Revision.Mode != toolcontract.PublishedDiskRevision || source.Revision.Representation != toolcontract.UTF8SourceBytes {
		t.Fatalf("disk capture = %+v", source.Revision)
	}
	testutil.FailErr(t, "record pins", f.recorder.RecordRevisionPins(ctx, f.execution, []toolcontract.ExecutionRevision{source.Revision, destination}))
	f.tctx.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
		cancel()
		return nil
	}
	_, err = f.write.Apply(ctx, f.pinnedWrite("a.txt", "interrupted\n", destination, source.Revision), f.tctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted write: %v", err)
	}
	entries, err := os.ReadDir(f.root)
	testutil.FailErr(t, "list root", err)
	if len(entries) != 1 || entries[0].Name() != "a.txt" || f.disk(t, "a.txt") != "base\n" {
		t.Fatalf("interruption left partial publication: %v %q", entries, f.disk(t, "a.txt"))
	}
	count, err := f.recorder.InterruptPrograms(t.Context(), f.session, "output:host-loss")
	testutil.FailErr(t, "reconcile host loss", err)
	if count != 1 {
		t.Fatalf("interrupted %d programs", count)
	}
	pins, err := f.recorder.RevisionPins(t.Context(), f.session, f.execution)
	testutil.FailErr(t, "pins survive interruption", err)
	if len(pins) != 2 || pins[0] != source.Revision {
		t.Fatalf("pins = %+v", pins)
	}
	f.tctx.Files.FileChangeReview = nil
	result, err := f.write.Apply(t.Context(), f.pinnedWrite("a.txt", "fresh\n", destination, source.Revision), f.tctx)
	testutil.FailErr(t, "a new operation still finds the pinned state intact", err)
	if !result.Saved || f.disk(t, "a.txt") != "fresh\n" {
		t.Fatalf("result = %+v", result)
	}
}
