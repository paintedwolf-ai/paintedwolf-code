package command

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
	"github.com/lycaon/lycaon/internal/serviceguest"
	"github.com/lycaon/lycaon/internal/testutil"
)

type privateServiceFixture struct {
	args     []string
	input    []byte
	closed   atomic.Bool
	closeErr error
	result   serviceguest.Result
}

func (f *privateServiceFixture) RunInput(_ context.Context, args []string, input []byte) (serviceguest.Result, error) {
	f.args = slices.Clone(args)
	f.input = slices.Clone(input)
	return f.result, nil
}

func (f *privateServiceFixture) Close() error { f.closed.Store(true); return f.closeErr }

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
		got, err := session.execute(t.Context(), request, commandprogram.Streams{Input: io.NopCloser(strings.NewReader("query input")), Output: &out, Error: &diag})
		testutil.FailErr(t, "run retained private client", err)
		if string(fixture.input) != "query input" || got.Status != 3 || got.Complete == truncated || got.PublicationEligible || out.String() != "answer" || diag.String() != "diagnostic" || !slices.Equal(fixture.args, []string{"query", "two words"}) {
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

func TestPrivateServiceCapacityFollowsEnvironmentLifetime(t *testing.T) {
	slots := make(chan struct{}, 1)
	first, second := &privateServiceSession{slots: slots}, &privateServiceSession{slots: slots}
	open := func(context.Context) (privateServiceClient, error) { return &privateServiceFixture{}, nil }
	_, err := first.acquire(t.Context(), open)
	testutil.FailErr(t, "reserve first service environment", err)
	if _, err := second.acquire(t.Context(), open); err == nil {
		t.Fatal("second environment exceeded capacity")
	}
	first.active = 1
	if _, err := first.releaseIdle(); err == nil {
		t.Fatal("released an active service environment")
	}
	first.active = 0
	_, err = first.releaseIdle()
	testutil.FailErr(t, "release idle environment", err)
	if _, err := second.acquire(t.Context(), func(context.Context) (privateServiceClient, error) { return nil, errors.New("missing payload") }); err == nil {
		t.Fatal("missing payload started")
	}
	_, err = second.acquire(t.Context(), open)
	testutil.FailErr(t, "reuse capacity after failed boot", err)
	testutil.FailErr(t, "close final environment", second.close())
	if len(slots) != 0 {
		t.Fatal("closed environment retained capacity")
	}
}

func TestPrivateServiceRejectsInvalidInputBeforeAcquiringRuntime(t *testing.T) {
	session := &privateServiceSession{closed: true}
	for _, tc := range []struct {
		name  string
		args  commandprogram.Fields
		input string
		code  string
	}{
		{"missing executable", commandprogram.Fields{{Value: "private-service"}, {Value: "--"}}, "", "COMMAND_OPERAND_INVALID"},
		{"input exceeds limit", commandprogram.Fields{{Value: "private-service"}, {Value: "--"}, {Value: "program"}}, strings.Repeat("x", serviceguest.MaxInput+1), "COMMAND_LIMIT_EXCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.execute(t.Context(), commandprogram.OperationRequest{Argv: tc.args}, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(tc.input))})
			if err == nil || result.StopCode != tc.code {
				t.Fatalf("outcome %+v, error %v", result, err)
			}
		})
	}
}

func TestPrivateServiceReclaimsSlotAfterHelperExitError(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	exitErr := errors.New("helper exited with status 1")
	fixture := &privateServiceFixture{closeErr: exitErr}
	session := &privateServiceSession{slots: slots, client: fixture}
	_, err := session.releaseIdle()
	if !errors.Is(err, exitErr) || !fixture.closed.Load() || len(slots) != 0 || session.client != nil {
		t.Fatalf("release after helper exit returned %v with %d occupied slots", err, len(slots))
	}
}
