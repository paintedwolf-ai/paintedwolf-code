package commandprogram_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

// TestStatusAlgebraMatchesBash evaluates every status source in every
// composition, the corpus the host's differential cuts to one source per owner
// kind, and requires bash's stored $? and PIPESTATUS reports. Owners are stubs
// that settle with the status the real command settles with, so a difference
// is the evaluator's.
func TestStatusAlgebraMatchesBash(t *testing.T) {
	stored, err := commandconformance.LoadSnapshots()
	testutil.FailErr(t, "load reference snapshots", err)
	seed, err := commandconformance.LoadGitSeed()
	testutil.FailErr(t, "load the Git seed", err)
	cases := commandconformance.StatusCases()
	fragments := 0
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			refs, err := stored.For(c, seed)
			testutil.FailErr(t, "stored snapshots", err)
			want := refs[referenceuserland.GNU]
			status, stdout := evaluateWithStubOwners(t, c.Program, c.Workspace(seed))
			if status != want.Status || stdout != string(want.Stdout) {
				t.Fatalf("status %d, want %d; stdout:\n%s\nwant:\n%s", status, want.Status, stdout, want.Stdout)
			}
			fragments += strings.Count(stdout, " status=") - 1
		})
	}
	t.Logf("compared %d status fragments in %d compositions", fragments, len(cases))
}

func evaluateWithStubOwners(t *testing.T, program string, workspace map[string]referenceuserland.Entry) (int, string) {
	t.Helper()
	ctx := t.Context()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load program memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create program memory", err)
	parsed, err := commandsurface.ParseAdmitted(ctx, memory, []byte(program), commandsurface.ParseOptions{SourceName: "program"})
	testutil.FailErr(t, "parse status program", err)
	defer parsed.Close()
	lease, err := parsed.SnapshotWithMemory(ctx, memory)
	testutil.FailErr(t, "snapshot status program", err)
	defer lease.Close()

	files := map[string]string{}
	for name, entry := range workspace {
		files[name] = string(entry.Data)
	}
	var stdout bytes.Buffer
	ports := commandprogram.EvaluationPorts{
		Invocation: toolcontract.OperationInvocation{
			Kind: toolcontract.ProgramChildInvocation, ActorID: "actor", SessionID: "session", SourceUnitID: "program",
			Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "execution"},
			Scope:    toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project"},
			Span:     toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest(),
		},
		Dispatch:    stubOwners{files: files},
		Binder:      commandprogram.CatalogBinder{},
		Files:       noGlobFiles{},
		Supervision: &runningJobs{},
		Predicates:  commandprogram.PredicateRuntime{Files: stubPaths{files: files}},
		Memory:      memory,
		Streams:     commandprogram.Streams{Input: commandprogram.NullInput(), Output: &stdout, Error: io.Discard},
	}
	outcome, err := commandprogram.Evaluate(ctx, lease.Plan, ports)
	testutil.FailErr(t, "evaluate status program", err)
	if outcome.Stop != nil {
		t.Fatalf("status program stopped: %+v", *outcome.Stop)
	}
	return outcome.Status, stdout.String()
}

// stubOwners settle each command the status corpus reaches with the status
// the real owner settles with in the reference workspace.
type stubOwners struct{ files map[string]string }

func (o stubOwners) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	return o, func() {}, nil
}

func (o stubOwners) InvokeEvaluator(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, body func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	streams, err := prepared(ctx, request, streams)
	if err != nil {
		return failed(1), err
	}
	return body(ctx, streams)
}

func (o stubOwners) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	streams, err := prepared(ctx, request, streams)
	if err != nil {
		return failed(1), err
	}
	request.Process.Times.Unmeasured()
	argv := request.Argv.Values()
	switch argv[0] {
	case "printf":
		_, err := io.WriteString(streams.Output, printfS(argv[1], argv[2:]))
		return settled(0), err
	case "cat":
		if len(argv) > 1 {
			if _, ok := o.files[argv[1]]; !ok {
				return settled(1), nil
			}
		}
		_, err := io.Copy(streams.Output, streams.Input)
		return settled(0), err
	case "grep":
		if strings.Contains(o.files[argv[len(argv)-1]], argv[len(argv)-2]) {
			return settled(0), nil
		}
		return settled(1), nil
	case "git":
		// The reference workspace is not a repository.
		return settled(128), nil
	}
	return failed(127), fmt.Errorf("no stub owner for %q", argv[0])
}

func (o stubOwners) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	streams, err := prepared(ctx, request, streams)
	if err != nil {
		return failed(1), err
	}
	argv := request.Argv.Values()
	if argv[0] == "external" {
		argv = argv[2:]
	}
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		return failed(127), fmt.Errorf("no stub launch for %q", argv)
	}
	switch script := argv[2]; {
	case script == "cat":
		_, err := io.Copy(streams.Output, streams.Input)
		return settled(0), err
	case script == ":":
		return settled(0), nil
	case strings.HasPrefix(script, "exit "):
		status, err := strconv.Atoi(strings.TrimPrefix(script, "exit "))
		return settled(status), err
	}
	return failed(127), fmt.Errorf("no stub script %q", argv[2])
}

func prepared(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (commandprogram.Streams, error) {
	if request.StreamPreparation == nil {
		return streams, nil
	}
	return request.StreamPreparation.PrepareStreams(ctx)
}

// printfS expands the %s, %% and \n the status reports use.
func printfS(format string, args []string) string {
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		switch {
		case strings.HasPrefix(format[i:], "%s"):
			if len(args) > 0 {
				out.WriteString(args[0])
				args = args[1:]
			}
			i++
		case strings.HasPrefix(format[i:], "%%"):
			out.WriteByte('%')
			i++
		case strings.HasPrefix(format[i:], `\n`):
			out.WriteByte('\n')
			i++
		default:
			out.WriteByte(format[i])
		}
	}
	return out.String()
}

func settled(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true,
		PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}
}

func failed(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
}

// runningJobs runs each started job at once with its own discarded streams,
// as the host's job registry does.
type runningJobs struct {
	mu      sync.Mutex
	started int
}

func (j *runningJobs) RunProgram(ctx context.Context, _ toolcontract.OperationInvocation, run func(context.Context) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
	return run(ctx)
}

func (j *runningJobs) StartJob(ctx context.Context, _ toolcontract.OperationInvocation, run func(context.Context, string, commandprogram.Streams) (toolcontract.ProducerResult, error)) (commandprogram.StartedJob, error) {
	j.mu.Lock()
	j.started++
	job := commandprogram.StartedJob{ID: strconv.Itoa(j.started), Handle: "job-" + strconv.Itoa(j.started)}
	j.mu.Unlock()
	go func() {
		_, _ = run(context.WithoutCancel(ctx), job.Handle, commandprogram.Streams{Input: commandprogram.NullInput(), Output: io.Discard, Error: io.Discard})
	}()
	return job, nil
}

// stubPaths answers test -f against the workspace's regular files.
type stubPaths struct{ files map[string]string }

func (p stubPaths) TestPath(_ context.Context, _ toolcontract.OperationInvocation, operator, path string) (bool, error) {
	_, ok := p.files[path]
	return ok && (operator == "-f" || operator == "-e"), nil
}

type noGlobFiles struct{}

func (noGlobFiles) ListDir(context.Context, string, int) ([]commandsurface.GlobEntry, error) {
	return nil, nil
}

func (noGlobFiles) Entry(context.Context, string) (commandsurface.GlobEntry, bool, error) {
	return commandsurface.GlobEntry{}, false, nil
}

func (noGlobFiles) Admit(context.Context, string) (bool, error) { return false, nil }
