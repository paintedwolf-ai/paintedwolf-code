package libraryworker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCloseStopsResidentWorker(t *testing.T) {
	input, output := io.Pipe()
	response, responseWriter := io.Pipe()
	defer responseWriter.Close()
	exited := make(chan struct{})
	close(exited)
	s := New(Options{ID: "fixture"})
	s.worker = &resident{stdin: output, stdout: bufio.NewReader(response), output: response, done: exited}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.worker != nil {
		t.Fatal("scanner still references its worker after close")
	}
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("worker stdin read after close = %v, want EOF", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestCloseJoinsActualResidentAndSealsRestart(t *testing.T) {
	scanner := New(Options{ID: "resident-close-fixture", Impl: "unknown-fixture-implementation"})
	t.Cleanup(func() { _ = scanner.Close() })
	if _, err := scanner.Run(t.Context(), scan.ScanRequest{}); err == nil {
		t.Fatal("unsupported implementation unexpectedly succeeded")
	}
	worker := scanner.worker
	if worker == nil || worker.exited() {
		t.Fatal("fixture did not leave the actual worker resident")
	}
	if err := scanner.Close(); err != nil {
		t.Fatalf("close resident scanner: %v", err)
	}
	if !worker.exited() {
		t.Fatal("Close returned before the actual process exited")
	}
	select {
	case <-worker.pipesClosed:
	default:
		t.Fatal("Close returned before resident pipes were joined")
	}
	if _, err := scanner.Run(t.Context(), scan.ScanRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("run closed scanner: got %v, want context.Canceled", err)
	}
	if scanner.worker != nil {
		t.Fatal("closed scanner created another resident")
	}
}

func TestRetireAllowsAdmittedScanToFinish(t *testing.T) {
	input, stdin := io.Pipe()
	output, response := io.Pipe()
	exited := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	scanner := New(Options{ID: "retiring-fixture"})
	scanner.worker = &resident{stdin: stdin, stdout: bufio.NewReader(output), output: output, done: exited}
	go func() {
		defer close(exited)
		defer response.Close()
		_, _ = bufio.NewReader(input).ReadBytes('\n')
		close(entered)
		<-release
		_, _ = response.Write([]byte("{\"result\":{}}\n"))
		_, _ = input.Read(make([]byte, 1))
	}()
	ran := make(chan error, 1)
	go func() { _, err := scanner.Run(t.Context(), scan.ScanRequest{}); ran <- err }()
	<-entered
	retired := make(chan error, 1)
	go func() { retired <- scanner.Retire(t.Context()) }()
	testutil.WaitFor(t, 5*time.Second, func() bool {
		_, finish, err := scanner.work.Begin(t.Context())
		if err == nil {
			finish()
			return false
		}
		return errors.Is(err, context.Canceled)
	})
	select {
	case err := <-ran:
		t.Fatalf("retirement interrupted admitted scan: %v", err)
	default:
	}
	close(release)
	if err := <-ran; err != nil {
		t.Fatalf("admitted scan: %v", err)
	}
	if err := <-retired; err != nil {
		t.Fatalf("retire: %v", err)
	}
	if _, err := scanner.Run(t.Context(), scan.ScanRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("retired scan admission: %v", err)
	}
}
