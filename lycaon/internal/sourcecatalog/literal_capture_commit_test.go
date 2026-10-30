package sourcecatalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCapturedContentRequiresCompleteAcceptedRead(t *testing.T) {
	for _, outcome := range []string{"accepted", "partial", "closed", "invalidated", "canceled", "oversize", "read-error", "short"} {
		t.Run(outcome, func(t *testing.T) {
			content := []byte("complete captured token")
			root, entry, index := captureFixture(t, string(content))
			version := index.cache.versionOf(root, entry)
			read := index.BeginRead(root, entry, version)
			defer read.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input := content
			if outcome == "oversize" {
				input = append(bytes.Clone(content), '!')
			}
			if outcome == "short" {
				input = input[:len(input)-1]
			}
			var source io.Reader = bytes.NewReader(input)
			if outcome == "read-error" {
				source = io.MultiReader(source, iotest.ErrReader(errors.New("capture read failed")))
			}
			stream := read.Wrap(ctx, source)
			if outcome == "partial" {
				_, err := io.ReadFull(stream, make([]byte, 3))
				testutil.FailErr(t, "read capture prefix", err)
			} else {
				_, err := io.Copy(io.Discard, stream)
				if outcome == "read-error" {
					if err == nil {
						t.Fatal("capture read error disappeared")
					}
				} else {
					testutil.FailErr(t, "read full capture", err)
				}
			}
			if read.Learned() || index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry, version) {
				t.Fatal("capture published before acceptance")
			}
			switch outcome {
			case "closed":
				read.Close()
			case "invalidated":
				index.cache.invalidate(root, []string{entry.Path})
			case "canceled":
				cancel()
			}
			if learned := read.Commit(); learned != (outcome == "accepted") {
				t.Fatalf("learned=%v outcome=%s", learned, outcome)
			}
			read.Close()
			if len(index.cache.fileReads) != 0 {
				t.Fatal("observation retained after settlement")
			}
			if outcome == "accepted" {
				if !index.CanPruneBytes(root, litprefilter.AnyOf("absent-token"), entry, version) || index.CanPruneBytes(root, litprefilter.AnyOf("captured"), entry, version) {
					t.Fatal("accepted capture did not retain complete literal facts")
				}
			}
		})
	}
}

func TestCapturedContentCaptureSettlementDuringRead(t *testing.T) {
	root, entry, index := captureFixture(t, "captured bytes")
	read := index.BeginRead(root, entry, index.cache.versionOf(root, entry))
	defer read.Close()
	input := &gatedCaptureReader{Reader: strings.NewReader("captured bytes"), entered: make(chan struct{}), release: make(chan struct{})}
	stream := read.Wrap(t.Context(), input)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, stream)
		done <- err
	}()
	<-input.entered
	if read.Commit() {
		t.Error("pending read was committed")
	}
	read.Close()
	close(input.release)
	testutil.FailErr(t, "finish closed capture read", <-done)
	if read.Commit() || len(index.cache.fileReads) != 0 || index.cache.files.count != 0 {
		t.Fatal("closed in-flight read published or retained an observation")
	}
}

func TestCapturedContentCaptureCommitAndCloseSerialize(t *testing.T) {
	root, entry, index := captureFixture(t, "captured bytes")
	read := index.BeginRead(root, entry, index.cache.versionOf(root, entry))
	_, err := io.Copy(io.Discard, read.Wrap(t.Context(), strings.NewReader("captured bytes")))
	testutil.FailErr(t, "finish concurrent settlement capture", err)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); <-start; read.Commit() }()
	go func() { defer workers.Done(); <-start; read.Close() }()
	close(start)
	workers.Wait()
	read.Close()
	if len(index.cache.fileReads) != 0 || index.cache.files.count > 1 || read.Learned() != (index.cache.files.count == 1) {
		t.Fatal("concurrent settlement retained or duplicated capture facts")
	}
}

func TestCapturedContentCaptureDoesNotRereadInput(t *testing.T) {
	content := strings.Repeat("captured bytes\n", 10000)
	root, entry, index := captureFixture(t, content)
	read := index.BeginRead(root, entry, index.cache.versionOf(root, entry))
	defer read.Close()
	input := &countedCaptureReader{Reader: strings.NewReader(content)}
	n, err := io.Copy(io.Discard, read.Wrap(t.Context(), input))
	testutil.FailErr(t, "consume one capture traversal", err)
	calls := input.calls
	if !read.Commit() || n != int64(len(content)) || input.bytes != len(content) || input.calls != calls {
		t.Fatalf("capture=%d input=%d reads=%d before commit=%d", n, input.bytes, input.calls, calls)
	}
}

type gatedCaptureReader struct {
	io.Reader
	entered, release chan struct{}
	once             sync.Once
}

func (r *gatedCaptureReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.Reader.Read(p)
}

type countedCaptureReader struct {
	io.Reader
	calls, bytes int
}

func (r *countedCaptureReader) Read(p []byte) (int, error) {
	r.calls++
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}
