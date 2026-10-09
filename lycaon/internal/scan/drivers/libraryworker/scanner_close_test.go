package libraryworker

import (
	"bufio"
	"errors"
	"io"
	"testing"
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
