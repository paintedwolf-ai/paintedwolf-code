package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
)

type fakeClock struct {
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	clock  *fakeClock
	at     time.Duration
	fire   func()
	active bool
}

func (c *fakeClock) after(d time.Duration, fire func()) timer {
	t := &fakeTimer{clock: c, at: c.now + d, fire: fire, active: true}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) advance(d time.Duration) {
	c.now += d
	for _, t := range c.timers {
		if t.active && c.now >= t.at {
			t.active = false
			t.fire()
		}
	}
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	was := t.active
	t.at, t.active = t.clock.now+d, true
	return was
}

func (t *fakeTimer) Stop() bool {
	was := t.active
	t.active = false
	return was
}

// step waits on the fake clock, then delivers its bytes unless the request was cancelled meanwhile.
type step struct {
	wait  time.Duration
	bytes string
}

type scriptedBody struct {
	ctx   context.Context
	clock *fakeClock
	steps []step
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if len(b.steps) == 0 {
		return 0, io.EOF
	}
	next := b.steps[0]
	b.clock.advance(next.wait)
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	b.steps = b.steps[1:]
	return copy(p, next.bytes), nil
}

func (b *scriptedBody) Close() error { return nil }

type scriptedTransport struct {
	clock *fakeClock
	steps []step
}

func (t *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := &scriptedBody{ctx: req.Context(), clock: t.clock, steps: t.steps}
	return &http.Response{StatusCode: http.StatusOK, Body: body, Request: req}, nil
}

func scriptedDownload(t *testing.T, steps []step) ([]byte, error) {
	t.Helper()
	clock := &fakeClock{}
	client := &http.Client{Transport: &stallTransport{
		base: &scriptedTransport{clock: clock, steps: steps}, stall: DownloadStall, after: clock.after,
	}}
	resp, err := client.Get("http://fixture.invalid/archive")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

func TestDownloadThatKeepsDeliveringBytesNeverFailsForElapsedTime(t *testing.T) {
	var steps []step
	var want bytes.Buffer
	// Two hours in all, each chunk arriving just inside the stall interval.
	for i := 0; i < 120; i++ {
		steps = append(steps, step{wait: DownloadStall - time.Second, bytes: "chunk"})
		want.WriteString("chunk")
	}
	got, err := scriptedDownload(t, steps)
	if err != nil {
		t.Fatalf("slow download failed: %v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("slow download delivered %d bytes, want %d", len(got), want.Len())
	}
}

func TestDownloadThatStopsDeliveringBytesFailsAsStalled(t *testing.T) {
	steps := []step{{bytes: "first"}, {bytes: "second"}, {wait: DownloadStall + time.Second, bytes: "late"}}
	got, err := scriptedDownload(t, steps)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("stalled download error = %v, want ErrStalled", err)
	}
	if string(got) != "firstsecond" || !strings.Contains(err.Error(), "after 11 bytes") {
		t.Fatalf("stalled download kept %q with %v, want the 11 bytes before the stall", got, err)
	}
}

func TestDownloadHasNoTotalTimeout(t *testing.T) {
	if got := Download(egressclass.BrowserEngineDownload).Timeout; got != 0 {
		t.Fatalf("Download timeout = %v, want 0: a total timeout fails a slow download that is still progressing", got)
	}
}
