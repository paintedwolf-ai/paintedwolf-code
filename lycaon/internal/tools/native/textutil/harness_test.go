//go:build darwin || linux

package textutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// spillQuota is a counting SpillAdmission with an explicit ceiling.
type spillQuota struct {
	maximum, used, peak int64
	reservations        int
}

func (q *spillQuota) ReserveSpill(_ context.Context, n int64) (func(), error) {
	if q.used+n > q.maximum {
		return nil, &toolcontract.ProgramMemoryLimitError{Account: "session_spill_bytes", Maximum: q.maximum, Observed: q.used + n}
	}
	q.used += n
	q.peak = max(q.peak, q.used)
	q.reservations++
	released := false
	return func() {
		if !released {
			released = true
			q.used -= n
		}
	}, nil
}

// batchLog is the evaluator's nested-command port: every batch is recorded
// with its own execution identity, and fixture commands write their argv.
type batchLog struct {
	mu      sync.Mutex
	batches [][]string
	status  map[string]int
	stop    string
}

// sorted returns the batches in argv order, since parallel batches launch
// concurrently.
func (b *batchLog) sorted() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := slices.Clone(b.batches)
	slices.SortStableFunc(out, func(x, y []string) int { return strings.Compare(strings.Join(x, "\x00"), strings.Join(y, "\x00")) })
	return out
}

func (b *batchLog) RunNested(_ context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	values := argv.Values()
	b.mu.Lock()
	b.batches = append(b.batches, values)
	b.mu.Unlock()
	if b.stop != "" && values[len(values)-1] == b.stop {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "PERMISSION_DENIED", EffectCertainty: toolcontract.NoEffect}, errors.New("refused batch")
	}
	_, err := io.WriteString(streams.Output, strings.Join(values, "|")+"\n")
	status := b.status[values[len(values)-1]]
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.EffectAttempted}, err
}

// pinLog follows the receipt store's contract: each capture of an
// execution, by any owner, appends at its next position.
type pinLog struct {
	executions map[string][]toolcontract.ExecutionRevision
}

func (l *pinLog) AppendRevisionPin(_ context.Context, execution string, pin toolcontract.ExecutionRevision) error {
	if err := pin.Validate(); err != nil {
		return err
	}
	if execution == "" {
		return io.ErrUnexpectedEOF
	}
	if l.executions == nil {
		l.executions = map[string][]toolcontract.ExecutionRevision{}
	}
	l.executions[execution] = append(l.executions[execution], pin)
	return nil
}

type harness struct {
	t           *testing.T
	root        string
	tc          tools.ToolContext
	owners      *Owners
	pins        *pinLog
	memory      *toolcontract.ProgramMemory
	spills      *Spills
	quota       *spillQuota
	nested      commandprogram.NestedCommand
	env         []string
	runs        int
	publication commandprogram.OutputPublication
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	argv           commandprogram.Fields
	execution      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	pins := &pinLog{}
	boundary := nativefixture.Boundary(t)
	owners, err := NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins)
	testutil.FailErr(t, "owners", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "memory", err)
	owners.Revisions.Memory = memory
	tc := nativefixture.Context(root)
	quota := &spillQuota{maximum: 1 << 30}
	mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary, Memory: memory}, pins, mutation.Operations{Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary},
		Move: &native.MoveTool{Boundary: boundary}, Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
		Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary}})
	testutil.FailErr(t, "mutation owners", err)
	mutations.Admission = &spillQuota{maximum: 1 << 30}
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "")
	testutil.FailErr(t, "program paths", err)
	publication := &mutation.Publication{Owners: mutations, Program: mutation.Program{Paths: paths}, Tools: tc, Admission: func(_ context.Context, _ commandprogram.OutputDestination, _ string) (tools.CommandOutputWrite, error) {
		return tools.CommandOutputWrite{Publish: func(ctx context.Context, apply func(context.Context, tools.ToolContext) (commandprogram.OutputPublished, error)) (commandprogram.OutputPublished, error) {
			return apply(ctx, tc)
		}, Abandon: func(context.Context, error) error { return nil }}, nil
	}}
	return &harness{t: t, root: root, tc: tc, owners: owners, pins: pins, memory: memory, spills: &Spills{}, quota: quota, publication: publication}
}

func (h *harness) seed(name, content string) {
	h.t.Helper()
	path := filepath.Join(h.root, name)
	testutil.FailErr(h.t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(h.t, "write "+name, os.WriteFile(path, []byte(content), 0o644))
}

func (h *harness) ports() Ports {
	return Ports{ToolContext: h.tc, Memory: h.memory, Spills: h.spills, Admission: h.quota, Nested: h.nested, Publication: h.publication}
}

func (h *harness) read(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.root, name))
	testutil.FailErr(h.t, "read "+name, err)
	return string(data)
}

func (h *harness) run(source, stdin string) run {
	h.t.Helper()
	return h.runWith(h.ports(), source, stdin)
}

// runWith parses one real program, expands its words through the word
// expander and dispatches the first command with stdin as its input.
func (h *harness) runWith(ports Ports, source, stdin string) run {
	h.t.Helper()
	request, release := h.bind(source)
	argv := request.Argv
	builtins := h.owners.Builtins(ports)
	var stdout, stderr bytes.Buffer
	result, err := builtins[argv[0].Value].Invoke(h.t.Context(), request, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(stdin)), Output: &stdout, Error: &stderr})
	release()
	if validation := result.Validate(); validation != nil {
		h.t.Fatalf("%s settled an invalid producer result %+v: %v", source, result, validation)
	}
	if used := ports.Memory.InUse().Working; used != 0 {
		h.t.Fatalf("%s retained %d bytes of working memory", source, used)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err, argv: argv, execution: request.Invocation.Identity.ExecutionID}
}

// bind owns the actual parsed command and its expansion frame.
func (h *harness) bind(source string) (commandprogram.OperationRequest, func()) {
	h.t.Helper()
	parsed, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "inline"})
	testutil.FailErr(h.t, "parse "+source, err)
	statement := parsed.Snapshot().Statements[0]
	command := statement.Command.(commandsurface.SimpleCommand)
	frame, err := commandprogram.NewVariables(h.t.Context(), h.memory, "inline", h.env, nil)
	testutil.FailErr(h.t, "frame", err)
	expander, err := commandprogram.NewWordExpander(commandprogram.ExpanderPorts{Substitutions: noSubstitution{}, Files: commandsurface.OSGlobFiles{Dir: h.root}, Memory: h.memory})
	testutil.FailErr(h.t, "expander", err)
	var argv commandprogram.Fields
	for _, word := range command.Words {
		fields, err := expander.ExpandFields(h.t.Context(), frame, word)
		testutil.FailErr(h.t, "expand", err)
		argv = append(argv, fields...)
	}
	environment, releaseEnvironment, err := frame.Environment(h.t.Context(), nil)
	testutil.FailErr(h.t, "environment", err)
	h.runs++
	execution := "child-" + strconv.Itoa(h.runs)
	request := commandprogram.OperationRequest{
		Invocation: toolcontract.OperationInvocation{
			Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: string(statement.ID), ExecutionID: execution},
			ActorID: "actor", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project"},
			SourceUnitID: "inline", Span: statement.Span, CatalogDigest: toolcontract.LanguageCatalogDigest(),
		},
		OperationID: argv[0].Value, Statement: statement, Argv: argv, Environment: environment,
	}
	return request, func() {
		releaseEnvironment()
		frame.Close()
	}
}

type noSubstitution struct{}

func (noSubstitution) Substitute(context.Context, commandprogram.ExpansionScope, commandprogram.SubstitutionRequest, io.Writer) (toolcontract.ProducerResult, error) {
	return toolcontract.ProducerResult{}, errors.New("fixtures run no substitutions")
}

func (r run) expectOutput(t *testing.T, label, stdout string, status int) {
	t.Helper()
	if r.err != nil || r.stdout != stdout || r.result.Status != status || r.result.Terminal != toolcontract.ProducerCompleted || r.result.PublicationEligible != (status == 0) {
		t.Fatalf("%s: stdout=%q stderr=%q result=%+v err=%v; want stdout=%q status=%d", label, r.stdout, r.stderr, r.result, r.err, stdout, status)
	}
}

func asMemoryLimit(err error, target **toolcontract.ProgramMemoryLimitError) bool {
	return errors.As(err, target)
}

func (r run) expectRefusal(t *testing.T, label, code string) *toolrejection.ToolReject {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != code {
		t.Fatalf("%s: result=%+v err=%v stdout=%q; want refusal %s", label, r.result, r.err, r.stdout, code)
	}
	return reject
}

// golden is one byte-exact case: a real program, its stdin and expected stdout.
type golden struct {
	source, stdin, stdout string
	status                int
}

func (h *harness) goldens(t *testing.T, cases []golden) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			h.run(c.source, c.stdin).expectOutput(t, c.source, c.stdout, c.status)
		})
	}
}
