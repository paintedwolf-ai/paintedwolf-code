package commandprogram

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// dispatchCall is what the fake owner observed for one dispatch.
type dispatchCall struct {
	operation   string
	argv        []string
	environment []string
	cwd         string
	stdin       string
	external    bool
	execution   toolcontract.ExecutionIdentity
	sourceUnit  string
	process     ChildProcess
	err         error
}

type dispatchLog struct {
	mu       sync.Mutex
	files    map[string]string
	calls    []dispatchCall
	started  chan string
	forks    int
	released int
	// clock, when set, advances while the burn fixture runs.
	clock *manualClock
}

func (l *dispatchLog) add(c dispatchCall) {
	l.mu.Lock()
	l.calls = append(l.calls, c)
	l.mu.Unlock()
}

func (l *dispatchLog) argv() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	for i, c := range l.calls {
		out[i] = strings.Join(c.argv, " ")
	}
	return out
}

func (l *dispatchLog) find(t *testing.T, operation string) dispatchCall {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		if c.operation == operation {
			return c
		}
	}
	t.Fatalf("no %s dispatch in %v", operation, l.calls)
	return dispatchCall{}
}

type codedError struct{ code string }

func (e *codedError) Error() string            { return e.code }
func (e *codedError) MachineErrorCode() string { return e.code }

// fakeDispatch is one directory frame of a fixture owner set. Its utilities
// move real bytes through the supplied streams.
type fakeDispatch struct {
	log *dispatchLog
	cwd string
}

func (d *fakeDispatch) Fork(context.Context) (Dispatch, func(), error) {
	d.log.mu.Lock()
	d.log.forks++
	d.log.mu.Unlock()
	return &fakeDispatch{log: d.log, cwd: d.cwd}, func() {
		d.log.mu.Lock()
		d.log.released++
		d.log.mu.Unlock()
	}, nil
}

func (d *fakeDispatch) Invoke(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			var failure toolcontract.ProducerFailure
			if errors.As(err, &failure) {
				return failure.ProducerResult(), err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	if err := request.Validate(); err != nil {
		return toolcontract.ProducerResult{}, err
	}
	argv := request.Argv.Values()
	// Like the host dispatcher, an in-process owner's time is not its own.
	request.Process.Times.Unmeasured()
	call := dispatchCall{operation: request.OperationID, argv: argv, environment: request.Environment, cwd: d.cwd, execution: request.Invocation.Identity, sourceUnit: request.Invocation.SourceUnitID, process: request.Process}
	if d.log.started != nil {
		d.log.started <- argv[0]
	}
	result, err := d.run(ctx, request, argv, request.Environment, streams, &call)
	call.err = err
	d.log.add(call)
	return result, err
}

func (d *fakeDispatch) run(ctx context.Context, req OperationRequest, argv, env []string, streams Streams, call *dispatchCall) (toolcontract.ProducerResult, error) {
	write := func(text string) (toolcontract.ProducerResult, error) {
		if _, err := io.WriteString(streams.Output, text); err != nil {
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
		return completed(0), nil
	}
	switch argv[0] {
	case "basename":
		return write("item\n")
	case "echo":
		return write(strings.Join(argv[1:], " ") + "\n")
	case "pwd":
		return write(d.cwd + "\n")
	case "cd":
		d.cwd = "/" + argv[len(argv)-1]
		return completed(0), nil
	case "env":
		return write(strings.Join(env, "\n") + "\n")
	case "cat":
		var operands Fields
		for _, field := range req.Argv[1:] {
			if field.Value == "--" {
				continue
			}
			operands = append(operands, field)
		}
		if len(operands) > 0 {
			var out strings.Builder
			for _, field := range operands {
				if strings.HasPrefix(field.Value, "--file=") {
					field = field.SliceFrom(len("--file="))
				}
				if subst := field.ProcessSubstitution(); subst != nil {
					if reader, ok := subst.OpenReader(); ok {
						data, err := io.ReadAll(reader)
						_ = reader.Close()
						if err != nil {
							return ownerResult(err), err
						}
						out.Write(data)
						continue
					}
				}
				d.log.mu.Lock()
				content, ok := d.log.files[field.Value]
				d.log.mu.Unlock()
				if !ok {
					return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, errors.New("no such file")
				}
				out.WriteString(content)
			}
			return write(out.String())
		}
		data, err := io.ReadAll(streams.Input)
		call.stdin = string(data)
		if err != nil {
			return ownerResult(err), err
		}
		return write(string(data))
	case "head":
		in := streams.Input
		if len(req.Argv) > 1 {
			if subst := req.Argv[len(req.Argv)-1].ProcessSubstitution(); subst != nil {
				if reader, ok := subst.OpenReader(); ok {
					in = reader
				}
			}
		}
		line, _ := bufio.NewReader(in).ReadString('\n')
		call.stdin = line
		_ = in.Close()
		return write(line)
	case "grep":
		pattern := ""
		if len(req.Argv) > 1 {
			pattern = req.Argv[1].Value
			if strings.HasPrefix(req.Argv[1].Value, "--file=") {
				field := req.Argv[1].SliceFrom(len("--file="))
				if subst := field.ProcessSubstitution(); subst != nil {
					if reader, ok := subst.OpenReader(); ok {
						raw, err := io.ReadAll(reader)
						_ = reader.Close()
						if err != nil {
							return ownerResult(err), err
						}
						pattern = strings.TrimSuffix(string(raw), "\n")
					}
				}
			}
		}
		data, err := io.ReadAll(streams.Input)
		call.stdin = string(data)
		if err != nil {
			return ownerResult(err), err
		}
		var matched []string
		for _, line := range strings.SplitAfter(string(data), "\n") {
			if line != "" && strings.Contains(line, pattern) {
				matched = append(matched, line)
			}
		}
		result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, PublicationBasis: toolcontract.NativeComparisonPublication, EffectCertainty: toolcontract.NoEffect}
		if len(matched) > 0 {
			result.Status = 0
			if _, err := io.WriteString(streams.Output, strings.Join(matched, "")); err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
			}
		}
		return result, nil
	case "sleep":
		<-ctx.Done()
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
	case "rm":
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "TOOL_NOT_OFFERED", EffectCertainty: toolcontract.NoEffect}, &codedError{"TOOL_NOT_OFFERED"}
	}
	return toolcontract.ProducerResult{}, fmt.Errorf("fixture has no %s owner", argv[0])
}

// Launch is the confined external fixture: fail N exits N, yes floods stdout.
func (d *fakeDispatch) Launch(ctx context.Context, request OperationRequest, streams Streams) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			var failure toolcontract.ProducerFailure
			if errors.As(err, &failure) {
				return failure.ProducerResult(), err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	if err := request.Validate(); err != nil {
		return toolcontract.ProducerResult{}, err
	}
	argv := request.Argv.Values()
	call := dispatchCall{operation: request.OperationID, argv: argv, environment: request.Environment, cwd: d.cwd, external: true, execution: request.Invocation.Identity, sourceUnit: request.Invocation.SourceUnitID, process: request.Process}
	defer func() { d.log.add(call) }()
	program := argv
	if argv[0] == "external" {
		program = argv[2:]
	}
	switch program[0] {
	case "burn":
		// A process that reports processor time when reaped.
		if d.log.clock != nil {
			d.log.clock.advance(1500 * time.Millisecond)
		}
		request.Process.Times.Add(2500*time.Millisecond, 250*time.Millisecond)
		return completed(0), nil
	case "fail":
		status, _ := strconv.Atoi(program[1])
		return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.EffectCompleted}, nil
	case "yes":
		for {
			if _, err := io.WriteString(streams.Output, "y\n"); err != nil {
				call.err = err
				return toolcontract.ProducerResult{Status: 141, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectCompleted}, err
			}
		}
	}
	return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, fmt.Errorf("not found")
}

type fakePublication struct {
	mu        sync.Mutex
	published map[string]string
	abandoned []string
}

type fakeStaged struct {
	owner  *fakePublication
	target string
	append bool
	buf    bytes.Buffer
}

func (p *fakePublication) Open(_ context.Context, destination OutputDestination) (StagedOutput, error) {
	switch destination.Target.Value {
	case "denied":
		return nil, errors.New("permission denied")
	case "refused":
		return nil, &codedError{"COMMAND_PATH_REFUSED"}
	}
	return &fakeStaged{owner: p, target: destination.Target.Value, append: strings.HasSuffix(destination.Redirect.Operator, ">>")}, nil
}
func (s *fakeStaged) Write(b []byte) (int, error) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	return s.buf.Write(b)
}
func (s *fakeStaged) Publish(context.Context) (OutputPublished, error) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.owner.published == nil {
		s.owner.published = map[string]string{}
	}
	if s.append {
		s.owner.published[s.target] += s.buf.String()
	} else {
		s.owner.published[s.target] = s.buf.String()
	}
	return OutputPublished{PublishedRevision: "r1"}, nil
}
func (s *fakeStaged) Abandon(context.Context) error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.abandoned = append(s.owner.abandoned, s.target)
	return nil
}

type fakeInput map[string]string

func (f fakeInput) OpenInput(_ context.Context, destination InputDestination) (io.ReadCloser, error) {
	data, ok := f[destination.Target.Value]
	if !ok {
		return nil, errors.New("no such input")
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

// fakeSources serves captured scripts; after each capture it applies the
// optional change, so later bytes differ from what was captured.
type fakeSources struct {
	mu      sync.Mutex
	files   map[string]string
	changes map[string]string
}

func (s *fakeSources) Capture(_ context.Context, _ toolcontract.OperationInvocation, request SourceCaptureRequest) (CapturedSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[request.Path]
	if !ok {
		return CapturedSource{}, errors.New("no such script")
	}
	captured := capturedFixture([]byte(data))
	captured.Pin.SourceID = request.Path
	if next, ok := s.changes[request.Path]; ok {
		s.files[request.Path] = next
	}
	return captured, nil
}

// fakeJobs holds started jobs so a test controls when they run.
type fakeJobs struct {
	mu   sync.Mutex
	runs []func(context.Context, Streams) (toolcontract.ProducerResult, error)
}

func (j *fakeJobs) RunProgram(ctx context.Context, _ toolcontract.OperationInvocation, run func(context.Context) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	return run(ctx)
}
func (j *fakeJobs) StartJob(_ context.Context, _ toolcontract.OperationInvocation, run func(context.Context, string, Streams) (toolcontract.ProducerResult, error)) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	handle := fmt.Sprintf("job-%d", len(j.runs)+1)
	j.runs = append(j.runs, func(ctx context.Context, streams Streams) (toolcontract.ProducerResult, error) {
		return run(ctx, handle, streams)
	})
	return handle, nil
}

type evalFixture struct {
	ports       EvaluationPorts
	log         *dispatchLog
	stdout      *bytes.Buffer
	publication *fakePublication
	sources     *fakeSources
	jobs        *fakeJobs
}

func newEvalFixture(t testing.TB) *evalFixture {
	t.Helper()
	log := &dispatchLog{files: map[string]string{}}
	stdout := &bytes.Buffer{}
	f := &evalFixture{log: log, stdout: stdout, publication: &fakePublication{}, sources: &fakeSources{files: map[string]string{}, changes: map[string]string{}}, jobs: &fakeJobs{}}
	root := fixtureInvocation()
	root.SourceUnitID = "program"
	f.ports = EvaluationPorts{Invocation: root, Dispatch: &fakeDispatch{log: log, cwd: "/project"}, Binder: CatalogBinder{}, Files: memoryFiles{},
		Publication: f.publication, Input: fakeInput{"input.txt": "from file\n"}, Sources: f.sources, Supervision: f.jobs,
		Memory: fixtureScratch(t), Streams: Streams{Input: NullInput(), Output: stdout, Error: io.Discard}}
	return f
}

func (f *evalFixture) run(t testing.TB, ctx context.Context, program string) (ProgramOutcome, error) {
	t.Helper()
	parsed, err := commandsurface.ParseAdmitted(ctx, f.ports.Memory, []byte(program), commandsurface.ParseOptions{SourceName: "program"})
	testutil.FailErr(t, "parse fixture program", err)
	defer parsed.Close()
	lease, err := parsed.SnapshotWithMemory(ctx, f.ports.Memory)
	testutil.FailErr(t, "snapshot fixture plan", err)
	defer lease.Close()
	return Evaluate(ctx, lease.Plan, f.ports)
}

func (d *fakeDispatch) InvokeEvaluator(ctx context.Context, request OperationRequest, streams Streams, body func(context.Context, Streams) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	if request.StreamPreparation != nil {
		var err error
		streams, err = request.StreamPreparation.PrepareStreams(ctx)
		if err != nil {
			var failure toolcontract.ProducerFailure
			if errors.As(err, &failure) {
				return failure.ProducerResult(), err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}
	}
	return body(ctx, streams)
}
