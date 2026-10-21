package mutation

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/desktoptrash"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// pinLog follows the receipt store's contract: each capture appends at the
// execution's next position.
type pinLog struct {
	mu   sync.Mutex
	pins map[string][]toolcontract.ExecutionRevision
}

func (l *pinLog) AppendRevisionPin(_ context.Context, execution string, pin toolcontract.ExecutionRevision) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := pin.Validate(); err != nil {
		return err
	}
	if execution == "" {
		return io.ErrUnexpectedEOF
	}
	if l.pins == nil {
		l.pins = map[string][]toolcontract.ExecutionRevision{}
	}
	l.pins[execution] = append(l.pins[execution], pin)
	return nil
}

type harness struct {
	t       *testing.T
	db      *db.Store
	root    string
	tc      tools.ToolContext
	owners  *Owners
	pins    *pinLog
	program Program
	service *projectsource.SourceMutationService
	project *project.Project
}

// newHarness wires the shared owners to a real attached root, its source
// ledger and the Files history service, with a system Trash that must never
// be reached, and a real session scratch directory.
func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	raw := t.TempDir()
	root, err := filepath.EvalSymlinks(raw)
	testutil.FailErr(t, "resolve temp root", err)
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(name, "/")))
		testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(full), 0o755))
		if strings.HasSuffix(name, "/") {
			testutil.FailErr(t, "mkdir", os.MkdirAll(full, 0o755))
		} else {
			testutil.FailErr(t, "seed", os.WriteFile(full, []byte(content), 0o644))
		}
	}
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at) VALUES ('r1', 'p1', ?, 'root', 1, ?)`, root, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root", err)
	ledger := sourceledger.New(sqlDB, t.TempDir())
	service := projectsource.NewSourceMutationService(ledger.LedgerDB(), ledger)
	service.Effects.SetTrashMover(func(context.Context, string) (desktoptrash.Receipt, error) {
		t.Fatal("a command mutation reached the system Trash")
		return desktoptrash.Receipt{}, nil
	})
	tc := nativefixture.Context(root)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.UserTurn = "p1", "s1", 1
	tc.Source.SourceLedger, tc.Source.SourceMutations = ledger, service
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.GitMutations, tc.Source.Observations = ledger.Commands, ledger.Git, ledger.Inventory
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve scratch", err)
	tc.Host.SessionScratchDir = scratch
	boundary := mutationBoundary()
	pins := &pinLog{}
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins, Operations{Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary},
		Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
		Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary}})
	testutil.FailErr(t, "build owners", err)
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "")
	testutil.FailErr(t, "bind program paths", err)
	p := &project.Project{ID: "p1", Roots: []project.Root{{ID: "r1", ProjectID: "p1", Path: root, IsPrimary: true}}}
	return &harness{t: t, db: sqlDB, root: root, tc: tc, owners: owners, pins: pins, program: Program{Paths: paths, Barrier: &native.PublicationBarrier{}}, service: service, project: p}
}

// mutationBoundary admits every operation the frontend reaches for the default
// profile, so refusals in these tests come from scope and confinement.
func mutationBoundary() *sandbox.Boundary {
	allowed := map[string]bool{}
	for _, name := range []string{"read", "write", "edit", "delete", "copy", "move", "mkdir", "chmod", "chown", "extract_archive", "stat", "list_dir"} {
		allowed[name] = true
	}
	return sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: allowed}})
}

func (h *harness) invocation() toolcontract.OperationInvocation {
	return toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID: "actor", SessionID: "s1", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	execution      string
}

// run binds and invokes one utility the way native dispatch does.
func (h *harness) run(stdin string, words ...string) run {
	h.t.Helper()
	var stdout bytes.Buffer
	r := h.runAs(h.invocation(), &stdout, stdin, words...)
	r.stdout = stdout.String()
	return r
}

// runAs invokes one utility as the given execution with stdout bound to out.
func (h *harness) runAs(inv toolcontract.OperationInvocation, out io.Writer, stdin string, words ...string) run {
	h.t.Helper()
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	request := commandprogram.OperationRequest{Invocation: inv, OperationID: words[0], Statement: statement, Argv: argv}
	prepared, err := h.owners.Prepare(h.program, request)
	if err != nil {
		return run{err: err, execution: inv.Identity.ExecutionID}
	}
	var stderr bytes.Buffer
	result, err := prepared.InvokeOwner(h.t.Context(), h.tc, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: out, Error: &stderr})
	if validErr := result.Validate(); validErr != nil {
		h.t.Fatalf("%v produced an invalid result %+v: %v", words, result, validErr)
	}
	return run{stderr: stderr.String(), result: result, err: err, execution: inv.Identity.ExecutionID}
}

func (h *harness) path(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h *harness) read(rel string) string {
	h.t.Helper()
	data, err := os.ReadFile(h.path(rel))
	testutil.FailErr(h.t, "read "+rel, err)
	return string(data)
}

func (h *harness) absent(rel string) bool {
	_, err := os.Lstat(h.path(rel))
	return os.IsNotExist(err)
}

func (h *harness) publication() *Publication {
	return &Publication{Owners: h.owners, Program: h.program, Tools: h.tc}
}

func (h *harness) destination(operator, target string) commandprogram.OutputDestination {
	inv := h.invocation()
	return commandprogram.OutputDestination{Invocation: inv, Redirect: commandsurface.ProgramRedirect{Operator: operator, Descriptor: -1}, Target: commandprogram.Field{Value: target, Span: inv.Span}}
}

// undoAll restores the latest n Files history entries, newest first.
func (h *harness) undoAll(n int) {
	h.t.Helper()
	for i := 0; i < n; i++ {
		history, err := h.service.History.State(h.t.Context(), h.project.ID)
		testutil.FailErr(h.t, "read history", err)
		if history.Undo == nil {
			h.t.Fatalf("history ran out after %d of %d restores", i, n)
		}
		_, err = h.service.Undo(h.t.Context(), uuid.NewString(), h.project, projectsource.SourceHistoryMutationRequest{ExpectedEntryID: history.Undo.ID})
		testutil.FailErr(h.t, "undo", err)
	}
}
