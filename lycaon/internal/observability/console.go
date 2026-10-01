package observability

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Console output is best effort: the file captures are the record, so a
// console that stops draining drops lines instead of blocking the writer.
const (
	consoleQueueLines   = 4096
	consoleQueueBytes   = 4 << 20
	consoleFlushTimeout = 2 * time.Second
)

type consoleLine struct {
	data    []byte
	flushed chan struct{}
}

type consoleSink struct {
	out     io.Writer
	queue   chan consoleLine
	pending atomic.Int64
	dropped atomic.Uint64
}

var (
	consoleOnce sync.Once
	console     *consoleSink
)

// ConsoleStderr returns the non-blocking writer for stderr-bound logs.
func ConsoleStderr() io.Writer {
	return consoleStderr()
}

func consoleStderr() *consoleSink {
	consoleOnce.Do(func() { console = newConsoleSink(os.Stderr, consoleQueueLines, consoleQueueBytes) })
	return console
}

// FlushConsole waits, bounded by consoleFlushTimeout, for queued output to reach stderr.
func FlushConsole() {
	ctx, cancel := context.WithTimeout(context.Background(), consoleFlushTimeout)
	defer cancel()
	_ = consoleStderr().flush(ctx)
}

func newConsoleSink(out io.Writer, maxLines, maxBytes int) *consoleSink {
	s := &consoleSink{out: out, queue: make(chan consoleLine, maxLines)}
	// pending crosses zero when the byte budget is exhausted.
	s.pending.Store(int64(-maxBytes))
	go s.drain()
	return s
}

// Write copies p because slog reuses its buffer after Handle returns.
func (s *consoleSink) Write(p []byte) (int, error) {
	n := int64(len(p))
	if s.pending.Add(n) > 0 {
		s.pending.Add(-n)
		s.dropped.Add(1)
		return len(p), nil
	}
	line := consoleLine{data: append([]byte(nil), p...)}
	select {
	case s.queue <- line:
	default:
		s.pending.Add(-n)
		s.dropped.Add(1)
	}
	return len(p), nil
}

func (s *consoleSink) drain() {
	for line := range s.queue {
		if line.flushed != nil {
			close(line.flushed)
			continue
		}
		if dropped := s.dropped.Swap(0); dropped > 0 {
			_, _ = fmt.Fprintf(s.out, "console output dropped %d lines while stderr was not draining\n", dropped)
		}
		_, _ = s.out.Write(line.data)
		s.pending.Add(-int64(len(line.data)))
	}
}

// flush resolves once every line queued before the call has been written.
func (s *consoleSink) flush(ctx context.Context) error {
	marker := consoleLine{flushed: make(chan struct{})}
	select {
	case s.queue <- marker:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-marker.flushed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
