package serviceguest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type fixture struct {
	mu      sync.Mutex
	jobs    map[string]Job
	started chan string
	stopped chan string
	closed  chan struct{}
}

func (f *fixture) Start(_ context.Context, id string, argv []string, input []byte) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j := Job{ID: id, State: Exited, Result: Result{Stdout: append([]byte(nil), input...)}}
	if argv[0] == "serve" {
		j.State = Running
		f.started <- id
	}
	f.jobs[id] = j
	return j, nil
}
func (f *fixture) Status(_ context.Context, id string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return Job{}, errors.New("missing")
	}
	return j, nil
}
func (f *fixture) Wait(ctx context.Context, id string) (Job, error) {
	<-ctx.Done()
	return Job{}, ctx.Err()
}
func (f *fixture) Stop(_ context.Context, id string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return Job{}, errors.New("missing")
	}
	j.State = Exited
	f.jobs[id] = j
	f.stopped <- id
	return j, nil
}
func (f *fixture) Close() error { close(f.closed); return nil }

func TestServiceSession(t *testing.T) {
	ctx := t.Context()
	host, guest := net.Pipe()
	exec := &fixture{jobs: make(map[string]Job), started: make(chan string, 4), stopped: make(chan string, 4), closed: make(chan struct{})}
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, guest, exec) }()
	client, err := Open(ctx, host)
	testutil.FailErr(t, "open service channel", err)
	defer client.Close()
	input := []byte{0, 1, 255, 10}
	r, err := client.RunInput(ctx, []string{"cat"}, input)
	if err != nil || !bytes.Equal(r.Stdout, input) {
		t.Fatalf("stdin = %q, %v", r.Stdout, err)
	}
	if _, err := client.RunInput(ctx, []string{"cat"}, make([]byte, MaxInput+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if _, err := client.Start(ctx, []string{""}); err == nil {
		t.Fatal("empty program accepted")
	}
	first, err := client.Start(ctx, []string{"serve"})
	testutil.FailErr(t, "start first service job", err)
	<-exec.started
	jobCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := client.Run(jobCtx, []string{"serve"}); done <- err }()
	second := <-exec.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled service job: got %v", err)
	}
	if got := <-exec.stopped; got != second || got == first.ID {
		t.Fatalf("stopped %s for %s", got, second)
	}
	running, err := client.Status(ctx, first.ID)
	if err != nil || running.State != Running {
		t.Fatalf("other job %+v %v", running, err)
	}
	if _, err := client.Stop(ctx, first.ID); err != nil {
		testutil.FailErr(t, "stop first service job", err)
	}
	testutil.FailErr(t, "close service channel", client.Close())
	<-exec.closed
	if err := <-served; !errors.Is(err, io.EOF) {
		t.Fatalf("serve disconnected channel: got %v", err)
	}
}

func TestFrameBoundsAndHandshake(t *testing.T) {
	var req request
	for _, raw := range [][]byte{{0, 0, 0, 0}, {255, 255, 255, 255}, {0, 0, 0, 2, '{'}, {0, 0, 0, 4, '{', '}', '{', '}'}} {
		if err := readFrame(bytes.NewReader(raw), &req); err == nil {
			t.Fatalf("accepted %v", raw)
		}
	}
	ctx := t.Context()
	a, b := net.Pipe()
	defer a.Close()
	go func() {
		defer b.Close()
		var req request
		if err := readFrame(b, &req); err == nil {
			_ = writeFrame(b, response{Version: Version, Sequence: req.Sequence, Challenge: "wrong"})
		}
	}()
	if _, err := Open(ctx, a); err == nil {
		t.Fatal("unmatched challenge accepted")
	}
}
