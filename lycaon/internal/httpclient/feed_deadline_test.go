package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type feedContextReader struct{ ctx context.Context }

func (r feedContextReader) Read([]byte) (int, error) { <-r.ctx.Done(); return 0, r.ctx.Err() }

func TestFeedDeadlineCoversDNSHeadersAndBody(t *testing.T) {
	for _, phase := range []string{"dns", "headers", "body"} {
		t.Run(phase, func(t *testing.T) {
			opts := feedTestOptions()
			opts.Timeout = 100 * time.Millisecond
			var b *feedTestBody
			tr := &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
				if phase == "headers" {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				b = &feedTestBody{Reader: feedContextReader{ctx: r.Context()}}
				return &http.Response{StatusCode: 200, Body: b}, nil
			}}
			network := feedTestNetwork(tr)
			if phase == "dns" {
				network.resolve = func(ctx context.Context, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }
			}
			body, err := getFeed(t.Context(), "https://8.8.8.8/feed", opts, network)
			if !errors.Is(err, context.DeadlineExceeded) || body != nil {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if phase != "dns" && tr.closed != 1 {
				t.Fatalf("transport closes=%d", tr.closed)
			}
			if b != nil && !b.closed {
				t.Fatal("timed out body was not closed")
			}
		})
	}
}

func TestFeedRedirectsShareCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	tr := &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || !got.Equal(deadline) {
			t.Fatalf("hop deadline=%v want %v", got, deadline)
		}
		calls++
		resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}
		if calls <= 2 {
			resp.StatusCode = 302
			resp.Header = http.Header{"Location": {"/next"}}
		}
		return resp, nil
	}}
	opts := feedTestOptions()
	opts.Timeout = time.Minute
	_, err := getFeed(ctx, "https://8.8.8.8/feed", opts, feedTestNetwork(tr))
	testutil.FailErr(t, "fetch with earlier caller deadline", err)
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestFeedDeadlineDoesNotRestartAfterRedirect(t *testing.T) {
	var deadline time.Time
	calls := 0
	network := feedNetwork{
		resolve: func(ctx context.Context, _ string) ([]netip.Addr, error) {
			got, ok := ctx.Deadline()
			if !ok {
				t.Fatal("DNS has no deadline")
			}
			if deadline.IsZero() {
				deadline = got
			} else if !deadline.Equal(got) {
				t.Fatalf("deadline restarted: %v -> %v", deadline, got)
			}
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		transport: func(string, []netip.Addr) feedTransport {
			return &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
				got, _ := r.Context().Deadline()
				if !got.Equal(deadline) {
					t.Fatalf("HTTP deadline=%v DNS deadline=%v", got, deadline)
				}
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/next"}}, Body: http.NoBody}, nil
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}}
		},
	}
	opts := feedTestOptions()
	opts.Timeout = 100 * time.Millisecond
	_, err := getFeed(t.Context(), "https://8.8.8.8/feed", opts, network)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestFeedCancellation(t *testing.T) {
	t.Run("before resolution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := getFeed(ctx, "https://8.8.8.8/feed", feedTestOptions(), feedNetwork{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("during request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tr := &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		_, err := getFeed(ctx, "https://8.8.8.8/feed", feedTestOptions(), feedTestNetwork(tr))
		if !errors.Is(err, context.Canceled) || tr.closed != 1 {
			t.Fatalf("err=%v closed=%d", err, tr.closed)
		}
	})
}
