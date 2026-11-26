package command_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type recordingPythonRunner struct {
	mu       sync.Mutex
	requests []command.PythonExecutionRequest
	fn       func(context.Context, command.PythonExecutionRequest) (command.PythonExecutionResponse, error)
}

func (r *recordingPythonRunner) ExecuteCommand(ctx context.Context, req command.PythonExecutionRequest) (command.PythonExecutionResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	if r.fn != nil {
		return r.fn(ctx, req)
	}
	return command.PythonExecutionResponse{ExitCode: 0, Stdout: "wasi\n"}, nil
}

func (r *recordingPythonRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *recordingPythonRunner) last() command.PythonExecutionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

func newPythonCommandFixture(t *testing.T, runner command.PythonRunner, directOffered bool) *programtest.Fixture {
	t.Helper()
	fx := programtest.New(t, programtest.Options{
		Publication: func(dir string) commandprogram.OutputPublication {
			return fileOutputPublication{dir: dir}
		},
	})
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec)
	testutil.FailErr(t, "compile profile effects", err)
	fx.Runtime.Boundary.SetProfileSource(func(context.Context, string) []sandbox.ToolProfile {
		return []sandbox.ToolProfile{{
			ID:             "fixture",
			Tools:          map[string]bool{"command": true, "python": true, "verify": true, "summarize": true, "survey_repo": true, "code_rewrite": true},
			CommandEffects: classes,
		}}
	})
	fx.Runtime.PortablePython = runner
	fx.Runtime.DirectPythonOffered = func(tools.ToolContext) bool { return directOffered }
	return fx
}

type fileOutputPublication struct{ dir string }

func (p fileOutputPublication) Open(_ context.Context, dest commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	return &fileStage{path: filepath.Join(p.dir, dest.Target.Value)}, nil
}

type fileStage struct {
	path string
	buf  []byte
}

func (s *fileStage) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *fileStage) Publish(context.Context) (commandprogram.OutputPublished, error) {
	if err := os.WriteFile(s.path, s.buf, 0o600); err != nil {
		return commandprogram.OutputPublished{}, err
	}
	return commandprogram.OutputPublished{}, nil
}

func (s *fileStage) Abandon(context.Context) error { return nil }

func TestBarePythonCommandRoutesToPythonToolRequiredAndPreservesPriorEffects(t *testing.T) {
	runner := &recordingPythonRunner{}
	fx := newPythonCommandFixture(t, runner, true)

	out, err := fx.Call(t, t.Context(), `echo hi > a.txt && python3 -c 'print(1)'`)
	if err != nil {
		testutil.FailErr(t, "Call compound command", err)
	}
	if !strings.Contains(out, pythoncatalog.RoutingCodePythonToolRequired) {
		t.Fatalf("expected %s refusal in output, got:\n%s", pythoncatalog.RoutingCodePythonToolRequired, out)
	}
	if runner.count() != 0 {
		t.Fatalf("interpreter launched %d times, want 0 before PYTHON_TOOL_REQUIRED stop", runner.count())
	}
	data, readErr := os.ReadFile(filepath.Join(fx.Dir, "a.txt"))
	if readErr != nil {
		testutil.FailErr(t, "read prior compound effect a.txt", readErr)
	}
	if string(data) != "hi\n" {
		t.Fatalf("a.txt = %q, want %q", string(data), "hi\n")
	}
}

func TestExternalPythonAndProjectInterpreterBypassRoutingGuard(t *testing.T) {
	runner := &recordingPythonRunner{}
	fx := newPythonCommandFixture(t, runner, true)

	// 1. Explicit project interpreter script at ./.venv/bin/python
	venvBin := filepath.Join(fx.Dir, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o750); err != nil {
		testutil.FailErr(t, "mkdir .venv/bin", err)
	}
	projPy := filepath.Join(venvBin, "python")
	if err := os.WriteFile(projPy, []byte("#!/bin/sh\necho project-venv-python\n"), 0o700); err != nil {
		testutil.FailErr(t, "write fake project python", err)
	}
	out, err := fx.Call(t, t.Context(), `./.venv/bin/python -c 'print(1)'`)
	if err != nil {
		testutil.FailErr(t, "Call ./.venv/bin/python", err)
	}
	if strings.Contains(out, pythoncatalog.RoutingCodePythonToolRequired) {
		t.Fatalf("explicit project interpreter must not trigger PYTHON_TOOL_REQUIRED:\n%s", out)
	}
	if !strings.Contains(out, "project-venv-python") {
		t.Fatalf("expected project interpreter stdout, got:\n%s", out)
	}
	if runner.count() != 0 {
		t.Fatalf("portable runner should not run for explicit project interpreter, got %d calls", runner.count())
	}

	// 2. Explicit `external -- python3` bypasses PYTHON_TOOL_REQUIRED and runs external process
	out, err = fx.Call(t, t.Context(), `external -- python3 -c 'import sys; print(sys.platform)'`)
	if err != nil {
		testutil.FailErr(t, "Call external -- python3", err)
	}
	if strings.Contains(out, pythoncatalog.RoutingCodePythonToolRequired) {
		t.Fatalf("external -- python3 must not trigger PYTHON_TOOL_REQUIRED:\n%s", out)
	}
	if runner.count() != 0 {
		t.Fatalf("portable runner should not run for external -- python3, got %d calls", runner.count())
	}
}

func TestCommandLanguagePythonDispatchesToPortablePythonNeverHostFallback(t *testing.T) {
	runner := &recordingPythonRunner{
		fn: func(_ context.Context, req command.PythonExecutionRequest) (command.PythonExecutionResponse, error) {
			return command.PythonExecutionResponse{
				ExitCode: 0,
				Stdout:   "wasi:" + req.Code + ":" + strings.Join(req.Args, ",") + "\n",
			}, nil
		},
	}
	fx := newPythonCommandFixture(t, runner, false)

	out, err := fx.Call(t, t.Context(), `python3 -c 'import sys; print(sys.platform)' --extra-arg 42`)
	if err != nil {
		testutil.FailErr(t, "Call python3 via command-language", err)
	}
	if runner.count() != 1 {
		t.Fatalf("portable runner calls = %d, want 1 (out=%s)", runner.count(), out)
	}
	got := runner.last()
	if got.Mode != pythonruntime.ModeCode || got.Code != "import sys; print(sys.platform)" || !slices.Equal(got.Args, []string{"--extra-arg", "42"}) {
		t.Fatalf("unexpected request: %+v", got)
	}
	if !strings.Contains(out, "wasi:import sys; print(sys.platform):--extra-arg,42") {
		t.Fatalf("expected portable python output, got:\n%s", out)
	}

	// When PortablePython runner is nil, MUST fail closed (never silently fall back to host python3).
	unconfigured := newPythonCommandFixture(t, nil, false)
	out, err = unconfigured.Call(t, t.Context(), `python3 -c 'import sys; print(sys.platform)'`)
	if err != nil {
		testutil.FailErr(t, "Call unconfigured python3", err)
	}
	if !strings.Contains(out, "COMMAND_PLATFORM_UNAVAILABLE") {
		t.Fatalf("expected COMMAND_PLATFORM_UNAVAILABLE when runtime unconfigured (no host fallback), got:\n%s", out)
	}
	if strings.Contains(out, "darwin") || strings.Contains(out, "linux") {
		t.Fatalf("silent host python fallback detected:\n%s", out)
	}
}

func TestManagedPytestAndMarkdownItDispatchAndProjectPytestPreserved(t *testing.T) {
	runner := &recordingPythonRunner{
		fn: func(_ context.Context, req command.PythonExecutionRequest) (command.PythonExecutionResponse, error) {
			if req.Mode == pythonruntime.ModeCLI && req.CLI == "markdown-it" {
				body, _ := io.ReadAll(req.Stdin)
				return command.PythonExecutionResponse{
					ExitCode: 0,
					Stdout:   "<h1>" + strings.TrimSpace(string(body)) + "</h1>\n",
				}, nil
			}
			if req.Mode == pythonruntime.ModeModule && req.Module == "pytest" {
				return command.PythonExecutionResponse{
					ExitCode: 0,
					Stdout:   "portable-pytest:" + strings.Join(req.Args, " ") + "\n",
				}, nil
			}
			return command.PythonExecutionResponse{ExitCode: 1}, nil
		},
	}
	fx := newPythonCommandFixture(t, runner, true)

	// 1. Managed markdown-it via pipe
	out, err := fx.Call(t, t.Context(), `printf 'hello\n' | markdown-it`)
	if err != nil {
		testutil.FailErr(t, "Call markdown-it", err)
	}
	if !strings.Contains(out, "<h1>hello</h1>") {
		t.Fatalf("expected managed markdown-it output, got:\n%s", out)
	}

	// 2. Managed pytest (must NOT return COMMAND_NATIVE_FORM_UNSUPPORTED)
	out, err = fx.Call(t, t.Context(), `pytest -q -k smoke`)
	if err != nil {
		testutil.FailErr(t, "Call pytest", err)
	}
	if strings.Contains(out, "COMMAND_NATIVE_FORM_UNSUPPORTED") {
		t.Fatalf("managed pytest returned COMMAND_NATIVE_FORM_UNSUPPORTED:\n%s", out)
	}
	if !strings.Contains(out, "portable-pytest:-q -k smoke") {
		t.Fatalf("expected managed pytest output, got:\n%s", out)
	}

	// 3. Project-installed ./.venv/bin/pytest runs as explicit external process
	venvBin := filepath.Join(fx.Dir, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o750); err != nil {
		testutil.FailErr(t, "mkdir .venv/bin", err)
	}
	projPytest := filepath.Join(venvBin, "pytest")
	if err := os.WriteFile(projPytest, []byte("#!/bin/sh\necho project-venv-pytest\n"), 0o700); err != nil {
		testutil.FailErr(t, "write fake project pytest", err)
	}
	beforeCalls := runner.count()
	out, err = fx.Call(t, t.Context(), `./.venv/bin/pytest -q`)
	if err != nil {
		testutil.FailErr(t, "Call ./.venv/bin/pytest", err)
	}
	if !strings.Contains(out, "project-venv-pytest") {
		t.Fatalf("expected project pytest output, got:\n%s", out)
	}
	if runner.count() != beforeCalls {
		t.Fatalf("portable runner should not intercept ./.venv/bin/pytest")
	}
}
