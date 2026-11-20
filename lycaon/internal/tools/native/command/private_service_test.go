package command

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type privateServiceFixture struct {
	args   []string
	closed atomic.Bool
	result serviceguest.Result
}

func (f *privateServiceFixture) RunInput(_ context.Context, args []string, _ []byte) (serviceguest.Result, error) {
	f.args = slices.Clone(args)
	return f.result, nil
}

func (f *privateServiceFixture) Close() error { f.closed.Store(true); return nil }

func TestPrivateServiceUsesRetainedSessionTransport(t *testing.T) {
	fixture := &privateServiceFixture{result: serviceguest.Result{ExitCode: 3, Stdout: []byte("answer"), Stderr: []byte("diagnostic")}}
	session := &privateServiceSession{}
	open := func(context.Context) (privateServiceClient, error) { return fixture, nil }
	_, err := session.acquire(t.Context(), open)
	testutil.FailErr(t, "start private service environment", err)
	var out, diag bytes.Buffer
	request := commandprogram.OperationRequest{Argv: commandprogram.Fields{{Value: "private-service"}, {Value: "--"}, {Value: "query"}, {Value: "two words"}}}
	for _, truncated := range []bool{false, true} {
		fixture.result.StdoutTruncated = truncated
		out.Reset()
		diag.Reset()
		got, err := session.execute(t.Context(), request, commandprogram.Streams{Output: &out, Error: &diag})
		testutil.FailErr(t, "run retained private client", err)
		if got.Status != 3 || got.Complete == truncated || got.PublicationEligible || out.String() != "answer" || diag.String() != "diagnostic" || !slices.Equal(fixture.args, []string{"query", "two words"}) {
			t.Fatalf("private service result %+v, stdout %q, stderr %q, arguments %q", got, out.String(), diag.String(), fixture.args)
		}
	}
	testutil.FailErr(t, "release service environment", session.close())
	_, err = session.acquire(t.Context(), open)
	if !fixture.closed.Load() || !errors.Is(err, context.Canceled) {
		t.Fatalf("closed service acquired: %v", err)
	}
}

func TestPrivateServiceCloseCancelsPendingStartup(t *testing.T) {
	session := &privateServiceSession{}
	started := make(chan struct{})
	exited := make(chan error, 1)
	go func() {
		_, err := session.acquire(t.Context(), func(ctx context.Context) (privateServiceClient, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		exited <- err
	}()
	<-started
	testutil.FailErr(t, "close pending startup", session.close())
	if err := <-exited; !errors.Is(err, context.Canceled) {
		t.Fatalf("startup returned %v", err)
	}
}
