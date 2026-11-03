package mutation

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// programDispatch routes mutation utilities to the owners and keeps echo and
// fail as the only other producers, so publication is exercised end to end.
type programDispatch struct{ h *harness }

func (d programDispatch) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	return d, func() {}, nil
}

func (d programDispatch) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
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
	argv := request.Argv.Values()
	if argv[0] == "echo" {
		_, err := io.WriteString(streams.Output, strings.Join(argv[1:], " ")+"\n")
		return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
	}
	prepared, err := d.h.owners.Prepare(d.h.program, request)
	if err != nil {
		return toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_OPTION_UNSUPPORTED", EffectCertainty: toolcontract.NoEffect}, err
	}
	return prepared.InvokeOwner(ctx, d.h.tc, streams)
}

// Launch is an external producer that writes a prefix and then fails.
func (d programDispatch) Launch(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
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
	_, _ = io.WriteString(streams.Output, "partial prefix\n")
	return toolcontract.ProducerResult{Status: 3, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func (h *harness) evaluate(program string) (commandprogram.ProgramOutcome, error) {
	h.t.Helper()
	return h.evaluateTo(program, io.Discard)
}

// evaluateTo runs a program with its standard output bound to stdout.
func (h *harness) evaluateTo(program string, stdout io.Writer) (commandprogram.ProgramOutcome, error) {
	h.t.Helper()
	parsed, err := commandsurface.ParseProgram([]byte(program), commandsurface.ParseOptions{SourceName: "program"})
	testutil.FailErr(h.t, "parse", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(h.t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(h.t, "memory", err)
	publication := h.publication()
	return commandprogram.Evaluate(h.t.Context(), parsed.Snapshot(), commandprogram.EvaluationPorts{Invocation: h.invocation(), Dispatch: programDispatch{h}, Binder: commandprogram.CatalogBinder{},
		Files: noGlobs{}, Publication: publication, Input: publication, Memory: memory, Streams: commandprogram.Streams{Output: stdout, Error: io.Discard}})
}

type noGlobs struct{}

func (noGlobs) ListDir(context.Context, string, int) ([]commandsurface.GlobEntry, error) {
	return nil, nil
}
func (noGlobs) Entry(context.Context, string) (commandsurface.GlobEntry, bool, error) {
	return commandsurface.GlobEntry{}, false, nil
}
func (noGlobs) Admit(context.Context, string) (bool, error) { return true, nil }

func TestProgramRedirectsPublishThroughTheWriteDoor(t *testing.T) {
	h := newHarness(t, map[string]string{"existing.txt": "keep me\n", "tree/a.txt": "a\n"})
	out, err := h.evaluate("echo hi > out.txt; echo more >> out.txt; tee copy.txt < out.txt > /dev/null; external-producer > existing.txt; echo $? > status.txt; rm -rf tree")
	if err != nil || out.Stop != nil {
		t.Fatalf("evaluate: %v %+v", err, out.Stop)
	}
	if h.read("out.txt") != "hi\nmore\n" || h.read("copy.txt") != "hi\nmore\n" || h.read("status.txt") != "3\n" || !h.absent("tree") {
		t.Fatalf("published out=%q copy=%q status=%q", h.read("out.txt"), h.read("copy.txt"), h.read("status.txt"))
	}
	// Q04: an unclassified nonzero producer never publishes its prefix.
	if h.read("existing.txt") != "keep me\n" {
		t.Fatalf("failed producer replaced the destination: %q", h.read("existing.txt"))
	}
}

func TestProgramRedirectConflictStopsLaterStatements(t *testing.T) {
	h := newHarness(t, map[string]string{"out.txt": "captured\n"})
	h.tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
		return os.WriteFile(h.path("out.txt"), []byte("human\n"), 0o644)
	}
	out, err := h.evaluate("echo program > out.txt; echo never > later.txt")
	if err == nil || out.Stop == nil || !h.absent("later.txt") || h.read("out.txt") != "human\n" {
		t.Fatalf("conflict did not stop the program: %v %+v", err, out.Stop)
	}
	var refusal interface{ MachineErrorCode() string }
	if !errors.As(err, &refusal) {
		t.Fatalf("conflict lost its typed code: %v", err)
	}
}

func TestProgramOutsideWritesRefusedForRedirectAndMutationWhileTempAllowed(t *testing.T) {
	h := newHarness(t, nil)
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "look up account home", err)
	outsideDir, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-mutation-outside-") //nolint:usetesting // Must lie outside OS temporary write roots.
	testutil.FailErr(t, "create outside fixture dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(outsideDir) })

	// The OS temporary directory in its own spelling, which on macOS runs
	// through a linked parent (/tmp or /var).
	tmpDir := t.TempDir()

	tmpFile := filepath.Join(tmpDir, "allowed.txt")
	tmpSub := filepath.Join(tmpDir, "sub")
	out, err := h.evaluate("echo temp-ok > " + tmpFile + "; mkdir " + tmpSub)
	if err != nil || out.Stop != nil {
		t.Fatalf("OS temp write refused below Strict: err=%v stop=%+v", err, out.Stop)
	}
	if data, readErr := os.ReadFile(tmpFile); readErr != nil || string(data) != "temp-ok\n" {
		t.Fatalf("temp redirect write = %q, %v", data, readErr)
	}
	if info, statErr := os.Stat(tmpSub); statErr != nil || !info.IsDir() {
		t.Fatalf("temp mkdir = %v, %v", info, statErr)
	}

	redirectTarget := filepath.Join(outsideDir, "redirect.txt")
	out, err = h.evaluate("echo nope > " + redirectTarget)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || out.Stop == nil {
		t.Fatalf("outside redirect was not refused with SURVEY_PATH_ESCAPE: err=%v stop=%+v", err, out.Stop)
	}
	if _, statErr := os.Lstat(redirectTarget); !os.IsNotExist(statErr) {
		t.Fatalf("outside redirect created %s: %v", redirectTarget, statErr)
	}

	mkdirTarget := filepath.Join(outsideDir, "created-dir")
	out, err = h.evaluate("mkdir " + mkdirTarget)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || out.Stop == nil {
		t.Fatalf("outside mkdir was not refused with SURVEY_PATH_ESCAPE: err=%v stop=%+v", err, out.Stop)
	}
	if _, statErr := os.Lstat(mkdirTarget); !os.IsNotExist(statErr) {
		t.Fatalf("outside mkdir created %s: %v", mkdirTarget, statErr)
	}

	teeTarget := filepath.Join(outsideDir, "tee.txt")
	out, err = h.evaluate("echo nope | tee " + teeTarget)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" || out.Stop == nil {
		t.Fatalf("outside tee was not refused with SURVEY_PATH_ESCAPE: err=%v stop=%+v", err, out.Stop)
	}
	if _, statErr := os.Lstat(teeTarget); !os.IsNotExist(statErr) {
		t.Fatalf("outside tee created %s: %v", teeTarget, statErr)
	}
}

func (d programDispatch) InvokeEvaluator(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams, body func(context.Context, commandprogram.Streams) (toolcontract.ProducerResult, error)) (toolcontract.ProducerResult, error) {
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
