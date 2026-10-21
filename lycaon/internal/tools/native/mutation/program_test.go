package mutation

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// programDispatch routes mutation utilities to the owners and keeps echo and
// fail as the only other producers, so publication is exercised end to end.
type programDispatch struct{ h *harness }

func (d programDispatch) Fork(context.Context) (commandprogram.Dispatch, func(), error) {
	return d, func() {}, nil
}

func (d programDispatch) Invoke(ctx context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
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
func (d programDispatch) Launch(_ context.Context, request commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	_, _ = io.WriteString(streams.Output, "partial prefix\n")
	return toolcontract.ProducerResult{Status: 3, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, nil
}

func (h *harness) evaluate(program string) (commandprogram.ProgramOutcome, error) {
	h.t.Helper()
	parsed, err := commandsurface.ParseProgram([]byte(program), commandsurface.ParseOptions{SourceName: "program"})
	testutil.FailErr(h.t, "parse", err)
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(h.t, "memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(h.t, "memory", err)
	publication := h.publication()
	return commandprogram.Evaluate(h.t.Context(), parsed.Snapshot(), commandprogram.EvaluationPorts{Invocation: h.invocation(), Dispatch: programDispatch{h}, Binder: commandprogram.CatalogBinder{},
		Files: noGlobs{}, Publication: publication, Input: publication, Memory: memory})
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
