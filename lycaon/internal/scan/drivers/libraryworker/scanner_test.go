package libraryworker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
)

func TestExchangeCancellationUnblocksPendingWrite(t *testing.T) {
	testExchangeCancellation(t, false)
}

func TestExchangeCancellationUnblocksPendingRead(t *testing.T) {
	testExchangeCancellation(t, true)
}

func testExchangeCancellation(t *testing.T, readRequest bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		input, output := io.Pipe()
		defer input.Close()
		defer output.Close()
		response, responseWriter := io.Pipe()
		defer response.Close()
		defer responseWriter.Close()
		stopped := make(chan struct{})
		close(stopped)
		worker := &resident{stdin: output, stdout: bufio.NewReader(response), output: response, done: stopped}
		if readRequest {
			go func() { _, _ = io.Copy(io.Discard, input) }()
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := worker.exchange(ctx, []byte(`{"impl":"fixture"}`))
			result <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("exchange cancellation: got %v, want context.Canceled", err)
			}
		default:
			t.Fatal("exchange remained blocked after cancellation")
		}
	})
}
