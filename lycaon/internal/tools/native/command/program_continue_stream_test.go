package command

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type continuationWriter struct {
	buffer  bytes.Buffer
	entered chan struct{}
	release chan struct{}
}

func (w *continuationWriter) Write(p []byte) (int, error) {
	if w.entered != nil {
		close(w.entered)
		<-w.release
		w.entered = nil
	}
	return w.buffer.Write(p)
}

func TestProgramContinuationStreamReplaysThenFollows(t *testing.T) {
	stream := newProgramStream(t.Context(), 1024, &outputSpillAccount{}, nil)
	defer stream.Close()
	_, err := stream.Write([]byte("before\n"))
	testutil.FailErr(t, "stage prefix", err)
	writer := &continuationWriter{entered: make(chan struct{}), release: make(chan struct{})}
	entered := writer.entered
	attached := make(chan error, 1)
	go func() { attached <- stream.follow(writer) }()
	<-entered
	appended := make(chan error, 1)
	go func() { _, err := stream.Write([]byte("after\n")); appended <- err }()
	close(writer.release)
	testutil.FailErr(t, "attach capture", <-attached)
	testutil.FailErr(t, "append during attachment", <-appended)
	if got := writer.buffer.String(); got != "before\nafter\n" {
		t.Fatalf("captured %q", got)
	}
}

type failedContinuationWriter struct{}

func (failedContinuationWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestProgramContinuationStreamReportsCaptureErrors(t *testing.T) {
	for _, state := range []string{"empty", "prefix", "closed"} {
		t.Run(state, func(t *testing.T) {
			stream := newProgramStream(t.Context(), 1024, &outputSpillAccount{}, nil)
			defer stream.Close()
			if state != "empty" {
				_, err := stream.Write([]byte("before"))
				testutil.FailErr(t, "stage prefix", err)
			}
			if state == "closed" {
				testutil.FailErr(t, "close spool", stream.spool.Close())
			}
			err := stream.follow(failedContinuationWriter{})
			if state == "empty" {
				testutil.FailErr(t, "attach empty stream", err)
				_, err = stream.Write([]byte("after"))
			}
			want := io.ErrClosedPipe
			if state == "closed" {
				want = os.ErrClosed
			}
			if !errors.Is(err, want) {
				t.Fatalf("capture error %v, want %v", err, want)
			}
		})
	}
}
