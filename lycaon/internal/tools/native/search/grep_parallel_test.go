package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/tools"
)

func parallelFixture(t *testing.T, h *harness, ctx context.Context, output io.Writer) *grepRun {
	t.Helper()
	prepared, err := h.owners.Prepare(mustPaths(t, h), h.dispatch, requestFor(h, "grep", "needle"))
	testutil.FailErr(t, "prepare grep", err)
	inv := newInvocation(ctx, prepared, h.tc, commandprogram.Streams{Output: output})
	matcher, err := textsearch.Compile(ctx, textsearch.Spec{Dialect: textsearch.Fixed, Patterns: []string{"needle"}}, h.owners.regex)
	testutil.FailErr(t, "compile grep", err)
	r := &grepRun{inv: inv, matcher: matcher, scan: textsearch.ScanOptions{MaxSelected: -1}, binaryMode: "text", walker: newWalker(inv)}
	t.Cleanup(r.walker.close)
	return r
}

func submitFiles(t *testing.T, r *grepRun, workers int, paths ...string) {
	t.Helper()
	var err error
	r.files, err = newGrepFiles(r, workers)
	testutil.FailErr(t, "start file workers", err)
	for _, path := range paths {
		e, err := r.walker.operand(path, false)
		testutil.FailErr(t, "bind file", err)
		testutil.FailErr(t, "submit file", r.files.submit(e, true, true))
	}
}

func TestGrepFileWorkersOverlapAndPublishInOrder(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle first\n", "b": "needle second\n"})
	started := make(chan string, 2)
	allow := make(chan struct{})
	ctx := tools.WithCommandReadObserver(t.Context(), func(ctx context.Context, _, path string) error {
		started <- path
		select {
		case <-allow:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	var output bytes.Buffer
	r := parallelFixture(t, h, ctx, &output)
	submitFiles(t, r, 2, "a", "b")
	paths := []string{<-started, <-started}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"a", "b"}) {
		t.Fatalf("concurrent reads = %v", paths)
	}
	close(allow)
	testutil.FailErr(t, "finish file workers", r.files.finish(nil))
	if got := output.String(); got != "a:needle first\nb:needle second\n" {
		t.Fatalf("ordered output = %q", got)
	}
	if got := h.pins.paths(r.inv.request.Invocation.Identity.ExecutionID); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ordered pins = %v", got)
	}
}

func TestGrepQuietMatchCancelsPendingReads(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle\n", "b": "needle later\n"})
	second := make(chan struct{})
	var canceled atomic.Bool
	ctx := tools.WithCommandReadObserver(t.Context(), func(ctx context.Context, _, path string) error {
		if path == "a" {
			select {
			case <-second:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		close(second)
		<-ctx.Done()
		canceled.Store(true)
		return ctx.Err()
	})
	var output bytes.Buffer
	r := parallelFixture(t, h, ctx, &output)
	r.mode = modeQuiet
	submitFiles(t, r, 2, "a", "b")
	testutil.FailErr(t, "finish quiet search", r.files.finish(nil))
	if !r.selected || !r.stopped || !canceled.Load() || output.Len() != 0 {
		t.Fatalf("quiet result: selected=%v stopped=%v canceled=%v output=%q", r.selected, r.stopped, canceled.Load(), output.String())
	}
}

type closedGrepOutput struct{}

func (closedGrepOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGrepDownstreamCloseCancelsPendingReads(t *testing.T) {
	h := newHarness(t, map[string]string{"a": strings.Repeat("needle line\n", 100), "b": "needle later\n"})
	second := make(chan struct{})
	var canceled atomic.Bool
	ctx := tools.WithCommandReadObserver(t.Context(), func(ctx context.Context, _, path string) error {
		if path == "a" {
			select {
			case <-second:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		close(second)
		<-ctx.Done()
		canceled.Store(true)
		return ctx.Err()
	})
	r := parallelFixture(t, h, ctx, closedGrepOutput{})
	submitFiles(t, r, 2, "a", "b")
	if err := r.files.finish(nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed downstream = %v", err)
	}
	if !canceled.Load() {
		t.Fatal("pending file read survived downstream close")
	}
}

func TestGrepCancellationReleasesQueuedFiles(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle\n", "b": "needle\n"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	var reads atomic.Int64
	ctx = tools.WithCommandReadObserver(ctx, func(ctx context.Context, _, _ string) error {
		reads.Add(1)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	r := parallelFixture(t, h, ctx, &bytes.Buffer{})
	var err error
	r.files, err = newGrepFiles(r, 1)
	testutil.FailErr(t, "start one worker", err)
	e, err := r.walker.operand("a", false)
	testutil.FailErr(t, "bind first file", err)
	testutil.FailErr(t, "submit first file", r.files.submit(e, true, true))
	<-started
	e, err = r.walker.operand("b", false)
	testutil.FailErr(t, "bind queued file", err)
	job := &grepFileJob{entry: e, events: make(chan grepFileEvent, grepEventWindow)}
	r.files.jobs <- job
	r.files.pending = append(r.files.pending, job)
	cancel()
	if err := r.files.finish(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation = %v", err)
	}
	if reads.Load() != 1 {
		t.Fatalf("reads after cancellation = %d", reads.Load())
	}
}

func TestGrepWorkerConstructionAndSubmitRespectCancellation(t *testing.T) {
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	r := parallelFixture(t, h, ctx, &bytes.Buffer{})
	cancel()
	if _, err := newGrepFiles(r, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled construction = %v", err)
	}
	live, stop := context.WithCancel(t.Context())
	defer stop()
	r = parallelFixture(t, h, live, &bytes.Buffer{})
	var err error
	r.files, err = newGrepFiles(r, 2)
	testutil.FailErr(t, "construct idle workers", err)
	stop()
	if err := r.files.submit(entry{}, true, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled submission = %v", err)
	}
	if err := r.files.finish(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled idle finish = %v", err)
	}
}
