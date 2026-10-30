package projectpaths_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestTypedNullOutputDoesNotAdmitAnOrdinaryPath(t *testing.T) {
	for _, bound := range []projectpaths.BoundPath{{}, {Resolved: projectpaths.Resolved{Abs: "/tmp/ordinary"}}} {
		if output, err := bound.OpenNullOutput(t.Context()); err == nil || output != nil {
			t.Fatal("ordinary path admitted as discard")
		}
	}
	bound := projectpaths.BoundPath{Null: true}
	if output, err := bound.OpenNullOutput(nil); err == nil || output != nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	output, err := bound.OpenNullOutput(ctx)
	if err != nil {
		testutil.FailErr(t, "open descriptor-backed null output", err)
	}
	data := []byte{'a', 0, 'b'}
	if n, err := output.Write(data); err != nil || n != len(data) {
		t.Fatalf("discard n=%d err=%v", n, err)
	}
	cancel()
	if n, err := output.Write(data); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled n=%d err=%v", n, err)
	}
	if err := output.Close(); err != nil {
		testutil.FailErr(t, "close canceled null output", err)
	}
	if err := output.Close(); err != nil {
		testutil.FailErr(t, "repeat idempotent null output close", err)
	}
	if n, err := output.Write(data); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed n=%d err=%v", n, err)
	}
	if next, err := bound.OpenNullOutput(ctx); next != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled context reopened sink")
	}
}
