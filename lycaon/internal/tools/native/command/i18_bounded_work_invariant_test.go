package command_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type i18CountingDispatch struct {
	commandprogram.Dispatch
	dispatches *atomic.Int64
	afterLimit *atomic.Int64
	blockArg   string
	started    chan struct{}
}

func newI18CountingDispatch(base commandprogram.Dispatch) *i18CountingDispatch {
	return &i18CountingDispatch{
		Dispatch:   base,
		dispatches: &atomic.Int64{},
		afterLimit: &atomic.Int64{},
	}
}

func (d *i18CountingDispatch) Invoke(ctx context.Context, req commandprogram.OperationRequest, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	d.dispatches.Add(1)
	if len(req.Argv) >= 2 && req.Argv[1].Value == "after_over" {
		d.afterLimit.Add(1)
	}
	if d.blockArg != "" && len(req.Argv) >= 2 && req.Argv[1].Value == d.blockArg {
		close(d.started)
		<-ctx.Done()
		return toolcontract.ProducerResult{Status: 130, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.NoEffect}, ctx.Err()
	}
	return d.Dispatch.Invoke(ctx, req, streams)
}

func (d *i18CountingDispatch) Fork(ctx context.Context) (commandprogram.Dispatch, func(), error) {
	child, release, err := d.Dispatch.Fork(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &i18CountingDispatch{
		Dispatch:   child,
		dispatches: d.dispatches,
		afterLimit: d.afterLimit,
		blockArg:   d.blockArg,
		started:    d.started,
	}, release, nil
}

type i18FilteredGlobFiles struct {
	entries []commandsurface.GlobEntry
	visited atomic.Int64
}

func (g *i18FilteredGlobFiles) ListDir(_ context.Context, dir string, maximum int) ([]commandsurface.GlobEntry, error) {
	if dir != "" {
		return nil, nil
	}
	limit := len(g.entries)
	if maximum >= 0 && limit > maximum+1 {
		limit = maximum + 1
	}
	g.visited.Add(int64(limit))
	return g.entries[:limit], nil
}

func (g *i18FilteredGlobFiles) Entry(_ context.Context, _ string) (commandsurface.GlobEntry, bool, error) {
	return commandsurface.GlobEntry{}, false, nil
}

func (g *i18FilteredGlobFiles) Admit(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func catalogLimitMax(t *testing.T, id string) int64 {
	t.Helper()
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	testutil.FailErr(t, "load catalog limits", err)
	for _, limit := range catalog.Language.Limits {
		if limit.ID == id {
			return limit.Maximum
		}
	}
	t.Fatalf("missing catalog limit %q", id)
	return 0
}

// I18 (Work limits measure performed work):
// Exercises real Portable Command operations through the Go resource harness
// at and over bounded charges with independent counters, release verification,
// cancellation cleanup, and post-refusal session reuse.
func TestBB10I18BoundedWorkChargeAndRelease(t *testing.T) {
	t.Run("memory_charge_at_over_and_release", assertI18MemoryChargeAndRelease)
	t.Run("filtered_traversal_visits_at_over_and_release", assertI18FilteredTraversalCharge)
	t.Run("cancellation_mid_charge_releases_and_reuses", assertI18CancellationReleasesCharge)
}

func assertI18MemoryChargeAndRelease(t *testing.T) {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	limits := toolcontract.ProgramMemoryLimits{Working: 8192, State: 16384}
	var activeMemory *toolcontract.ProgramMemory
	var spy *i18CountingDispatch

	f.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		mem, err := toolcontract.NewProgramMemory(limits)
		if err != nil {
			return commandprogram.ProgramOutcome{}, err
		}
		activeMemory = mem
		ports.Memory = mem
		spy = newI18CountingDispatch(ports.Dispatch)
		ports.Dispatch = spy
		return commandprogram.Evaluate(ctx, plan, ports)
	}

	// Within/at bounded working memory charge: succeeds and releases all leases.
	withinPayload := strings.Repeat("a", 2048)
	out, err := f.Call(t, t.Context(), fmt.Sprintf("x=$(echo %s); echo ok", withinPayload))
	testutil.FailErr(t, "run within memory bound", err)
	res := decodeOutcome(t, out)
	if res.State != "completed" || res.Status != 0 || res.Refusal != nil || res.Stdout != "ok\n" {
		t.Fatalf("within bound outcome = %+v", res)
	}
	if spy.dispatches.Load() < 2 || activeMemory.Reservations() == 0 {
		t.Fatalf("independent counters missed charge: dispatches=%d reservations=%d", spy.dispatches.Load(), activeMemory.Reservations())
	}
	if peak := activeMemory.Peak(); peak.Working == 0 || peak.Working > limits.Working || peak.State > limits.State {
		t.Fatalf("peak memory = %+v, want within %+v", peak, limits)
	}
	if inUse := activeMemory.InUse(); inUse.Working != 0 || inUse.State != 0 || activeMemory.AvailableWorking() != limits.Working {
		t.Fatalf("memory leaked after completion: inUse=%+v avail=%d", inUse, activeMemory.AvailableWorking())
	}

	// Over bounded working memory charge: refuses with COMMAND_LIMIT_EXCEEDED,
	// stops before later commands, and releases all held leases.
	overPayload := strings.Repeat("b", 5120)
	out, err = f.Call(t, t.Context(), fmt.Sprintf("x=$(echo %s); echo after_over", overPayload))
	testutil.FailErr(t, "run over memory bound", err)
	over := decodeOutcome(t, out)
	if over.State != "rejected" || over.Refusal == nil || over.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("over bound outcome = %+v", over)
	}
	if spy.afterLimit.Load() != 0 || strings.Contains(over.Stdout, "after_over") {
		t.Fatalf("post-limit command ran: afterLimit=%d stdout=%q", spy.afterLimit.Load(), over.Stdout)
	}
	if peak := activeMemory.Peak(); peak.Working > limits.Working || peak.State > limits.State {
		t.Fatalf("over-bound peak exceeded ceiling: peak=%+v limits=%+v", peak, limits)
	}
	if inUse := activeMemory.InUse(); inUse.Working != 0 || inUse.State != 0 || activeMemory.AvailableWorking() != limits.Working {
		t.Fatalf("memory leaked after refusal: inUse=%+v avail=%d", inUse, activeMemory.AvailableWorking())
	}

	// Subsequent unaffected command in the same session succeeds cleanly.
	reuseOut, err := f.Call(t, t.Context(), "echo recovered")
	testutil.FailErr(t, "run post-refusal reuse", err)
	reuse := decodeOutcome(t, reuseOut)
	if reuse.State != "completed" || reuse.Status != 0 || reuse.Stdout != "recovered\n" {
		t.Fatalf("reuse outcome = %+v", reuse)
	}
}

func assertI18FilteredTraversalCharge(t *testing.T) {
	t.Helper()
	maxVisits := int(catalogLimitMax(t, "traversal_visits"))
	makeEntries := func(total int) []commandsurface.GlobEntry {
		entries := make([]commandsurface.GlobEntry, total)
		entries[0] = commandsurface.GlobEntry{Name: "keep.match"}
		for i := 1; i < total; i++ {
			entries[i] = commandsurface.GlobEntry{Name: fmt.Sprintf("skip-%06d.txt", i)}
		}
		return entries
	}

	f := programtest.New(t, programtest.Options{})
	var globFiles *i18FilteredGlobFiles
	var activeMemory *toolcontract.ProgramMemory
	var spy *i18CountingDispatch

	f.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		activeMemory = ports.Memory
		if globFiles != nil {
			ports.Files = globFiles
		}
		spy = newI18CountingDispatch(ports.Dispatch)
		ports.Dispatch = spy
		return commandprogram.Evaluate(ctx, plan, ports)
	}

	// At exact traversal_visits bound: only 1 file matches, while independent
	// counter proves all maxVisits entries were visited and memory released.
	globFiles = &i18FilteredGlobFiles{entries: makeEntries(maxVisits)}
	out, err := f.Call(t, t.Context(), "echo *.match")
	testutil.FailErr(t, "run traversal at bound", err)
	at := decodeOutcome(t, out)
	if at.State != "completed" || at.Status != 0 || at.Stdout != "keep.match\n" {
		t.Fatalf("at-bound traversal outcome = %+v", at)
	}
	if got := globFiles.visited.Load(); got != int64(maxVisits) {
		t.Fatalf("at-bound visited entries = %d, want %d", got, maxVisits)
	}
	if inUse := activeMemory.InUse(); inUse.Working != 0 || inUse.State != 0 {
		t.Fatalf("memory leaked after at-bound traversal: %+v", inUse)
	}

	// Over traversal_visits bound with 100 extra filtered entries: final candidate
	// count is still 1, yet refusal fires at maxVisits+1 without runaway scan.
	globFiles = &i18FilteredGlobFiles{entries: makeEntries(maxVisits + 100)}
	out, err = f.Call(t, t.Context(), "echo *.match; echo after_over")
	testutil.FailErr(t, "run traversal over bound", err)
	over := decodeOutcome(t, out)
	if over.State != "rejected" || over.Refusal == nil || over.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" || !strings.Contains(over.Stderr, "traversal_visits") {
		t.Fatalf("over-bound traversal outcome = %+v", over)
	}
	if got := globFiles.visited.Load(); got != int64(maxVisits+1) {
		t.Fatalf("over-bound visited entries = %d, want bounded %d", got, maxVisits+1)
	}
	if spy.afterLimit.Load() != 0 || strings.Contains(over.Stdout, "after_over") {
		t.Fatalf("post-limit traversal command ran: afterLimit=%d stdout=%q", spy.afterLimit.Load(), over.Stdout)
	}
	if inUse := activeMemory.InUse(); inUse.Working != 0 || inUse.State != 0 {
		t.Fatalf("memory leaked after traversal refusal: %+v", inUse)
	}

	// Unaffected follow-up command succeeds cleanly in the same session.
	globFiles = nil
	reuseOut, err := f.Call(t, t.Context(), "echo after_traversal")
	testutil.FailErr(t, "run post-traversal reuse", err)
	reuse := decodeOutcome(t, reuseOut)
	if reuse.State != "completed" || reuse.Status != 0 || reuse.Stdout != "after_traversal\n" {
		t.Fatalf("post-traversal reuse = %+v", reuse)
	}
}

func assertI18CancellationReleasesCharge(t *testing.T) {
	t.Helper()
	f := programtest.New(t, programtest.Options{})
	limits := toolcontract.ProgramMemoryLimits{Working: 8192, State: 8192}
	var activeMemory *toolcontract.ProgramMemory
	started := make(chan struct{})
	blocking := true

	f.Runtime.Evaluate = func(ctx context.Context, plan commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		mem, err := toolcontract.NewProgramMemory(limits)
		if err != nil {
			return commandprogram.ProgramOutcome{}, err
		}
		activeMemory = mem
		ports.Memory = mem
		if blocking {
			d := newI18CountingDispatch(ports.Dispatch)
			d.blockArg = "hold"
			d.started = started
			ports.Dispatch = d
		}
		return commandprogram.Evaluate(ctx, plan, ports)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		raw, callErr := f.Call(t, ctx, "x=$(echo payload); echo hold; echo after_over")
		done <- raw
		errCh <- callErr
	}()
	testutil.AwaitClosed(t, "blocking stage started", started)
	cancel()

	raw := <-done
	callErr := <-errCh
	if callErr != nil {
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("canceled command error = %v, want context.Canceled", callErr)
		}
	} else {
		canceled := decodeOutcome(t, raw)
		if canceled.State != "interrupted" || strings.Contains(canceled.Stdout, "after_over") {
			t.Fatalf("canceled outcome = %+v", canceled)
		}
	}
	if inUse := activeMemory.InUse(); inUse.Working != 0 || inUse.State != 0 || activeMemory.AvailableWorking() != limits.Working {
		t.Fatalf("memory leaked after cancellation: inUse=%+v avail=%d", inUse, activeMemory.AvailableWorking())
	}

	blocking = false
	reuseOut, err := f.Call(t, t.Context(), "echo post_cancel")
	testutil.FailErr(t, "run post-cancel reuse", err)
	reuse := decodeOutcome(t, reuseOut)
	if reuse.State != "completed" || reuse.Status != 0 || reuse.Stdout != "post_cancel\n" {
		t.Fatalf("post-cancel reuse = %+v", reuse)
	}
}
