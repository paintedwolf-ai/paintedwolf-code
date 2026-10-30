package literal

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

// pinLog follows the receipt store's contract: each call appends one capture
// at the execution's next position.
type pinLog struct {
	mu   sync.Mutex
	pins map[string][]toolcontract.ExecutionRevision
}

func (l *pinLog) AppendRevisionPin(_ context.Context, execution string, pin toolcontract.ExecutionRevision) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if execution == "" {
		return io.ErrUnexpectedEOF
	}
	if err := pin.Validate(); err != nil {
		return err
	}
	if l.pins == nil {
		l.pins = map[string][]toolcontract.ExecutionRevision{}
	}
	l.pins[execution] = append(l.pins[execution], pin)
	return nil
}

// mappingBuffer is an output endpoint that retains candidate mappings.
type mappingBuffer struct {
	bytes.Buffer
	mappings []toolcontract.StreamSourceMapping
}

func (b *mappingBuffer) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	b.mappings = append(b.mappings, m)
	return nil
}

type harness struct {
	t        *testing.T
	boundary *sandbox.Boundary
	root     string
	tc       tools.ToolContext
	owners   *Owners
	pins     *pinLog
	cwd      string
	runs     int
}

type run struct {
	stdout, stderr string
	mappings       []toolcontract.StreamSourceMapping
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

func symlink(t *testing.T, target, path string) {
	t.Helper()
	testutil.FailErr(t, "symlink "+path, os.Symlink(target, path))
}

func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "resolve temp root", err)
	seed(t, resolved, files)
	boundary := nativefixture.Boundary(t)
	pins := &pinLog{}
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins, DomainOwners{})
	testutil.FailErr(t, "build owners", err)
	return &harness{t: t, boundary: boundary, root: resolved, tc: nativefixture.Context(resolved), owners: owners, pins: pins, cwd: "."}
}

func (h *harness) invocation() toolcontract.OperationInvocation {
	h.runs++
	return toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", ExecutionID: "execution-" + strings.Repeat("x", h.runs)}}
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
		fields[i] = commandprogram.Field{Value: word}
	}
	if _, ok := h.owners.Operation(argv[0]); !ok {
		h.t.Fatalf("%s has no operation", argv[0])
	}
	return commandprogram.OperationRequest{Invocation: h.invocation(), OperationID: argv[0], Argv: fields}
}

func streamsFor(stdin string) commandprogram.Streams {
	return commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &mappingBuffer{}, Error: &bytes.Buffer{}}
}

// exec runs one argv with stdin through Prepare and InvokeOwner.
func (h *harness) exec(stdin string, argv ...string) run {
	h.t.Helper()
	paths := mustPaths(h.t, h)
	request := requestFor(h, argv...)
	var stdout mappingBuffer
	var stderr bytes.Buffer
	streams := commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &stdout, Error: &stderr}
	result, err := h.owners.Invoke(h.t.Context(), h.tc, paths, request, streams)
	if invalid := result.Validate(); invalid != nil {
		h.t.Fatalf("%v: invalid producer %+v: %v", argv, result, invalid)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), mappings: stdout.mappings, result: result, err: err, execution: request.Invocation.Identity.ExecutionID}
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

// refused asserts a typed stop with its public code and no stdout.
func (r run) refused(t *testing.T, label, code string) {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != code || r.result.PublicationEligible {
		t.Fatalf("%s: want refusal %s, got result %+v err %v stdout %q", label, code, r.result, r.err, r.stdout)
	}
}
