package search

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// fixtureTime is the deterministic mtime every seeded entry carries.
var fixtureTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// pinLog follows the receipt store's contract: each call carries the
// captures from one ordinal on; a recorded position may only be repeated.
type pinLog struct {
	mu   sync.Mutex
	pins map[string][]toolcontract.ExecutionRevision
}

func (l *pinLog) RecordRevisionPins(_ context.Context, execution string, first int, pins []toolcontract.ExecutionRevision) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, pin := range pins {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	existing := l.pins[execution]
	if first < 0 || first > len(existing) || len(pins) == 0 {
		return io.ErrUnexpectedEOF
	}
	overlap := min(len(pins), len(existing)-first)
	if !slices.Equal(existing[first:first+overlap], pins[:overlap]) {
		return io.ErrUnexpectedEOF
	}
	if l.pins == nil {
		l.pins = map[string][]toolcontract.ExecutionRevision{}
	}
	l.pins[execution] = append(slices.Clone(existing), pins[overlap:]...)
	return nil
}

func (l *pinLog) paths(execution string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, pin := range l.pins[execution] {
		out = append(out, pin.Path)
	}
	return out
}

// removals is a recoverable-removal owner that records each request and
// removes the entry, refusing a non-empty directory as the rm owner does.
type removals struct {
	mu      sync.Mutex
	removed []string
	refuse  map[string]error
}

func (r *removals) RemoveRecoverable(_ context.Context, _ tools.ToolContext, target projectpaths.BoundPath) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refuse[target.Resolved.DisplayPath]; err != nil {
		return err
	}
	if err := os.Remove(target.Resolved.Abs); err != nil {
		if entries, _ := os.ReadDir(target.Resolved.Abs); len(entries) > 0 {
			return &toolrejection.ToolReject{Code: "DELETE_NOT_EMPTY", Data: map[string]any{"path": target.Resolved.DisplayPath}}
		}
		return err
	}
	r.removed = append(r.removed, target.Resolved.DisplayPath)
	return nil
}

// childDispatch stands in for the evaluator frame: it records each -exec
// child request, runs search natives through the same owners and echoes any
// other argv as "argv: WORD|WORD" so tests see literal boundaries.
type childDispatch struct {
	h        *harness
	mu       sync.Mutex
	requests []commandprogram.OperationRequest
	status   func(argv []string) int
}

func (d *childDispatch) record(request commandprogram.OperationRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, request)
}

func (d *childDispatch) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	d.record(request)
	if _, owned := d.h.owners.Operation(request.OperationID); owned {
		return d.h.owners.Invoke(ctx, d.h.tc, mustPaths(d.h.t, d.h), d, request, streams)
	}
	return d.echo(request, streams)
}

func (d *childDispatch) Launch(_ context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	d.record(request)
	return d.echo(request, streams)
}

func (d *childDispatch) echo(request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	argv := request.Argv.Values()
	if _, err := io.WriteString(streams.Output, "argv: "+strings.Join(argv, "|")+"\n"); err != nil {
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
	}
	status := 0
	if d.status != nil {
		status = d.status(argv)
	}
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}, nil
}

func (d *childDispatch) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	return d, func() {}, nil
}

type harness struct {
	t        *testing.T
	boundary *sandbox.Boundary
	root     string
	tc       tools.ToolContext
	owners   *Owners
	pins     *pinLog
	removals *removals
	dispatch *childDispatch
	cwd      string
	runs     int
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	execution      string
}

// seed writes files (a trailing "/" makes a directory) and pins every mtime.
func seed(t *testing.T, root string, files map[string]string) {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(name, "/")))
		testutil.FailErr(t, "mkdir parent "+name, os.MkdirAll(filepath.Dir(full), 0o755))
		if strings.HasSuffix(name, "/") {
			testutil.FailErr(t, "mkdir "+name, os.MkdirAll(full, 0o755))
		} else {
			testutil.FailErr(t, "write "+name, os.WriteFile(full, []byte(files[name]), 0o644))
		}
	}
	for i := len(names) - 1; i >= 0; i-- {
		full := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(names[i], "/")))
		testutil.FailErr(t, "chtimes "+names[i], os.Chtimes(full, fixtureTime, fixtureTime))
	}
}

func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve temp root", err)
	seed(t, resolved, files)
	boundary := nativefixture.Boundary(t)
	pins := &pinLog{}
	removed := &removals{}
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins, removed)
	testutil.FailErr(t, "build owners", err)
	owners.Now = func() time.Time { return fixtureTime.Add(36 * time.Hour) }
	h := &harness{t: t, boundary: boundary, root: resolved, tc: nativefixture.Context(resolved), owners: owners, pins: pins, removals: removed, cwd: "."}
	h.dispatch = &childDispatch{h: h}
	return h
}

func (h *harness) invocation() toolcontract.OperationInvocation {
	h.runs++
	return toolcontract.OperationInvocation{
		Kind: toolcontract.ProgramChildInvocation, ActorID: "actor", SessionID: "session", CatalogDigest: "catalog", SourceUnitID: "unit",
		Scope:    toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
		Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution-" + strings.Repeat("x", h.runs)},
		Span:     toolcontract.SourceSpan{StartByte: 0, EndByte: 4, Line: 1, Column: 1},
	}
}

func mustPaths(t *testing.T, h *harness) *projectpaths.ProgramPaths {
	t.Helper()
	paths, err := projectpaths.NewProgramPaths(t.Context(), h.boundary, h.tc, h.cwd)
	testutil.FailErr(t, "bind program cwd", err)
	return paths
}

func requestFor(h *harness, argv ...string) commandprogram.OperationRequest {
	fields := make(commandprogram.Fields, len(argv))
	for i, word := range argv {
		fields[i] = commandprogram.Field{Value: word, Span: toolcontract.SourceSpan{StartByte: i, EndByte: i + 1, Line: 1, Column: i + 1}}
	}
	spec, ok := h.owners.spec(argv[0])
	if !ok {
		h.t.Fatalf("%s is not a search command", argv[0])
	}
	operation := spec.Name
	inv := h.invocation()
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	return commandprogram.OperationRequest{Invocation: inv, OperationID: operation, Statement: statement, Argv: fields, Environment: []string{"LANG=C"}}
}

func streamsFor() commandprogram.Streams {
	return commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &bytes.Buffer{}, Error: &bytes.Buffer{}}
}

// exec runs one argv with stdin through Prepare and InvokeOwner.
func (h *harness) exec(stdin string, argv ...string) run {
	h.t.Helper()
	request := requestFor(h, argv...)
	var stdout, stderr bytes.Buffer
	input := commandprogram.NullInput()
	if stdin != "" {
		input = io.NopCloser(strings.NewReader(stdin))
	}
	streams := commandprogram.Streams{Input: input, Output: &stdout, Error: &stderr}
	result, err := h.owners.Invoke(h.t.Context(), h.tc, mustPaths(h.t, h), h.dispatch, request, streams)
	if invalid := result.Validate(); invalid != nil {
		h.t.Fatalf("%v: invalid producer %+v: %v", argv, result, invalid)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err, execution: request.Invocation.Identity.ExecutionID}
}

// expect asserts exact stdout, stderr and status for a completed run.
func (r run) expect(t *testing.T, label, stdout, stderr string, status int) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s: unexpected error %v", label, r.err)
	}
	if r.stdout != stdout || r.stderr != stderr || r.result.Status != status || r.result.Terminal != toolcontract.ProducerCompleted {
		t.Fatalf("%s:\nstdout %q\nwant   %q\nstderr %q want %q\nresult %+v want status %d", label, r.stdout, stdout, r.stderr, stderr, r.result, status)
	}
}

// refused asserts a typed stop with its public code and no publication.
func (r run) refused(t *testing.T, label, code string) {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != code || r.result.PublicationEligible {
		t.Fatalf("%s: want refusal %s, got result %+v err %v stdout %q", label, code, r.result, r.err, r.stdout)
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	testutil.FailErr(t, "symlink "+path, os.Symlink(target, path))
}
