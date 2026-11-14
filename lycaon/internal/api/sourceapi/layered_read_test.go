package sourceapi

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Fixture evidence: no kernel mount; live reads come from the Core a mount would serve.
func TestWorkerSourceReadGoesThroughTheViewOrItsCheckpoint(t *testing.T) {
	tmp := t.TempDir()
	source, mount := filepath.Join(tmp, "source"), filepath.Join(tmp, "mount")
	for _, dir := range []string{source, mount} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
		testutil.FailErr(t, "write app.go", os.WriteFile(filepath.Join(dir, "app.go"), []byte("trunk\n"), 0o644))
	}
	const rootID = "code"
	roots := []layeredfs.RootSpec{{ID: rootID, Label: rootID, TrunkPath: source, IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{ViewID: "view-read", BackingDir: filepath.Join(tmp, "backing"), Roots: roots})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: "app.go"}, []byte("captured\n"), 0o644)
	testutil.FailErr(t, "write captured", err)
	store, err := checkpoint.NewStore(filepath.Join(tmp, "checkpoints"))
	testutil.FailErr(t, "new store", err)
	publisher, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{Core: core, Store: store, WorkerID: "w", TaskID: "w", ProjectID: "p", SourceGeneration: "1", SelectedRoots: roots})
	testutil.FailErr(t, "new publisher", err)
	_, err = publisher.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp", Trigger: checkpoint.TriggerExplicitCapture})
	testutil.FailErr(t, "publish", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: "app.go"}, []byte("live\n"), 0o644)
	testutil.FailErr(t, "live edit", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: rootID, Rel: "bin.dat"}, []byte{0xff, 0xfe, 0x00}, 0o644)
	testutil.FailErr(t, "binary", err)
	engine, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID: "view-read", Roots: roots, MountedRootPaths: map[string]string{rootID: mount},
		BackingDir: filepath.Join(tmp, "backing"), Core: core, CheckpointStore: store,
	})
	testutil.FailErr(t, "new engine", err)
	consumer.RegisterBranchEngine(mount, engine)
	t.Cleanup(func() { consumer.UnregisterEngine(engine) })

	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, rootID, source)
	workspace := &Workspace{SourceLedger: sourceledger.New(database, filepath.Join(tmp, "content"))}
	proj := &project.Project{ID: testdbseed.DefaultProjectID, Roots: []project.Root{{ID: rootID, Path: mount, Label: rootID, IsPrimary: true}}}
	scope := sourceViewerWorkspace{id: "w", kind: wire.SourceWorkspaceKindWorker, branch: proj.BranchForRoot(rootID)}
	read := func(path, checkpointID string) (*wire.ProjectSourceReadResponse, *httptest.ResponseRecorder, bool, error) {
		recorder := httptest.NewRecorder()
		res, handled, err := workspace.readLayeredWorkerProjectSource(t.Context(), recorder, mount, checkpointID, proj, projectsource.SourceReadRequest{RootID: rootID, Path: path}, scope)
		return res, recorder, handled, err
	}

	live, recorder, handled, err := read("app.go", "")
	testutil.FailErr(t, "live read", err)
	if !handled || live.Content != "live\n" || recorder.Header().Get("X-Lycaon-Stored-In-Checkpoint") != "false" {
		t.Fatalf("live read handled=%v res=%+v headers=%v", handled, live, recorder.Header())
	}
	captured, recorder, _, err := read("app.go", "cp")
	testutil.FailErr(t, "checkpoint read", err)
	if captured.Content != "captured\n" || recorder.Header().Get("X-Lycaon-Stored-In-Checkpoint") != "true" {
		t.Fatalf("checkpoint read res=%+v headers=%v", captured, recorder.Header())
	}
	for name, c := range map[string]struct {
		path string
		want error
	}{
		"missing": {"gone.go", projectsource.ErrSourceNotFound},
		"empty":   {" / ", projectsource.ErrSourcePathInvalid},
		"binary":  {"bin.dat", projectsource.ErrSourceBinary},
		"escape":  {"../source/app.go", projectsource.ErrSourcePathDenied},
	} {
		if _, _, _, err := read(c.path, ""); !errors.Is(err, c.want) {
			t.Fatalf("%s read = %v, want %v", name, err, c.want)
		}
	}
	if _, handled, err := workspace.readLayeredWorkerProjectSource(t.Context(), nil, filepath.Join(tmp, "unregistered"), "", proj, projectsource.SourceReadRequest{Path: "app.go"}, scope); handled || err != nil {
		t.Fatalf("a branch with no engine was handled: %v", err)
	}
}

func TestWorkerChangesKeepWhatTheListingAlreadyNamed(t *testing.T) {
	if got := appendLayeredWorkerDivergenceChanges(t.Context(), " ", "", nil); got != nil {
		t.Fatalf("no branch = %+v", got)
	}
	named := []wire.WorkerJobChangedFile{{RootID: "code", Path: "a.go", Op: "modify"}}
	if got := appendLayeredWorkerDivergenceChanges(t.Context(), filepath.Join(t.TempDir(), "unregistered"), "", named); len(got) != 1 {
		t.Fatalf("a branch with no engine changed the listing: %+v", got)
	}
}
