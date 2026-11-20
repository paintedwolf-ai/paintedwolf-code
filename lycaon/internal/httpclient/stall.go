package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// DownloadStall is how long a download body may deliver no bytes before it fails.
const DownloadStall = 60 * time.Second

// ErrStalled reports a download body that delivered no bytes for its stall interval.
var ErrStalled = errors.New("download stalled")

type timer interface {
	Reset(time.Duration) bool
	Stop() bool
}

func afterTimer(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }

// stallTransport cancels a response whose body goes quiet for the stall interval.
type stallTransport struct {
	base  http.RoundTripper
	stall time.Duration
	after func(time.Duration, func()) timer
}

func (t *stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	body := &stallBody{body: resp.Body, cancel: cancel, stall: t.stall}
	body.timer = t.after(t.stall, body.expire)
	resp.Body = body
	return resp, nil
}

// CloseIdleConnections reaches the pooled transport, as http.Client expects.
func (t *stallTransport) CloseIdleConnections() {
	if pool, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		pool.CloseIdleConnections()
	}
}

type stallBody struct {
	body     io.ReadCloser
	cancel   context.CancelFunc
	stall    time.Duration
	timer    timer
	stalled  atomic.Bool
	received atomic.Int64
}

func (b *stallBody) expire() {
	b.stalled.Store(true)
	b.cancel()
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.received.Add(int64(n))
		b.timer.Reset(b.stall)
	}
	if err != nil && !errors.Is(err, io.EOF) && b.stalled.Load() {
		return n, fmt.Errorf("%w: no bytes for %s after %d bytes", ErrStalled, b.stall, b.received.Load())
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.timer.Stop()
	b.cancel()
	return b.body.Close()
}
