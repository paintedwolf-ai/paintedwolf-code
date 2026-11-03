package query

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
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// pinLog follows the receipt store's contract: each capture of an
// execution appends at its next position.
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
	root    string
	tc      tools.ToolContext
	owners  *Owners
	pins    *pinLog
	program Program
	// env is the program environment requests carry.
	env []string
}

// newHarness wires the owners to a real attached root with its source
// ledger and mutation service, as the shared write door requires.
func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp root", err)
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(full), 0o755))
		testutil.FailErr(t, "seed "+name, os.WriteFile(full, []byte(content), 0o644))
	}
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProject(t, sqlDB, "p1")
	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at) VALUES ('r1', 'p1', ?, 'root', 1, ?)`, root, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert root", err)
	ledger := sourceledger.New(sqlDB, t.TempDir())
	service := projectsource.NewSourceMutationService(ledger.LedgerDB(), ledger)
	tc := nativefixture.Context(root)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.UserTurn = "p1", "s1", 1
	tc.Source.SourceLedger, tc.Source.SourceMutations = ledger, service
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	boundary := nativefixture.Boundary(t)
	pins := &pinLog{}
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins, &native.WriteTool{Boundary: boundary})
	testutil.FailErr(t, "build owners", err)
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "")
	testutil.FailErr(t, "bind program paths", err)
	return &harness{t: t, root: root, tc: tc, owners: owners, pins: pins, program: Program{Paths: paths}}
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	execution      string
}

// exec binds and invokes one utility the way native dispatch does.
func (h *harness) exec(stdin string, words ...string) run {
	h.t.Helper()
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID: "actor", SessionID: "s1", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
	}
	if _, ok := h.owners.Operation(words[0]); !ok {
		h.t.Fatalf("%s has no operation", words[0])
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	request := commandprogram.OperationRequest{Invocation: inv, OperationID: words[0], Statement: statement, Argv: argv, Environment: h.env}
	var stdout, stderr bytes.Buffer
	result, err := h.owners.Invoke(h.t.Context(), h.tc, h.program, request, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &stdout, Error: &stderr})
	if invalid := result.Validate(); invalid != nil {
		h.t.Fatalf("%v produced an invalid result %+v: %v", words, result, invalid)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err, execution: inv.Identity.ExecutionID}
}

func (r run) expect(t *testing.T, label, stdout string, status int) {
	t.Helper()
	if r.err != nil || r.stdout != stdout || r.result.Status != status || r.result.Terminal != toolcontract.ProducerCompleted {
		t.Fatalf("%s:\nstdout %q\nwant   %q\nstatus %d want %d stderr %q err %v", label, r.stdout, stdout, r.result.Status, status, r.stderr, r.err)
	}
}

func (r run) refused(t *testing.T, label, code string) {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != code || r.result.PublicationEligible {
		t.Fatalf("%s: want refusal %s, got %+v err %v stdout %q", label, code, r.result, r.err, r.stdout)
	}
}

func (h *harness) disk(name string) string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, name))
	testutil.FailErr(h.t, "read "+name, err)
	return string(raw)
}
