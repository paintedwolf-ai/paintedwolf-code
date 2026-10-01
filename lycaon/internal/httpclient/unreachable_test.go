package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
)

// closedPortURL returns the address of a released listener.
func closedPortURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		testutil.FailErr(t, "listen for a free port", err)
	}
	url := "http://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		testutil.FailErr(t, "close listener", err)
	}
	return url
}

func TestUnreachableOnRefusedConnection(t *testing.T) {
	url := closedPortURL(t)
	_, err := Bounded(egressclass.ProviderModelDiscovery, 2*time.Second).Get(url) //nolint:bodyclose // the request is expected to fail
	if err == nil {
		t.Fatal("expected a dial error against a closed port")
	}
	if !Unreachable(err) {
		t.Fatalf("Unreachable(%v) = false, want true", err)
	}
}

func TestUnreachableOnBoundElapsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	_, err := Bounded(egressclass.ProviderModelDiscovery, 150*time.Millisecond).Get(srv.URL) //nolint:bodyclose // the request is expected to fail
	if err == nil {
		t.Fatal("expected a timeout against a hanging server")
	}
	if !Unreachable(err) {
		t.Fatalf("Unreachable(%v) = false, want true", err)
	}
}

func TestUnreachableIsFalseForAnsweredRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := Bounded(egressclass.ProviderModelDiscovery, 2*time.Second).Get(srv.URL)
	testutil.FailErr(t, "GET an answering server", err)
	defer func() { _ = resp.Body.Close() }()

	if Unreachable(err) {
		t.Fatal("a 503 response is not a transport failure")
	}
}

func TestUnreachableClassifiesWrappedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context deadline", context.DeadlineExceeded, true},
		{"wrapped context deadline", fmt.Errorf("discover models: %w", context.DeadlineExceeded), true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"wrapped connection refused", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), true},
		{"network unreachable", syscall.ENETUNREACH, true},
		{"host unreachable", syscall.EHOSTUNREACH, true},
		{"connection reset", syscall.ECONNRESET, true},
		{"broken pipe", syscall.EPIPE, true},
		{"context canceled", context.Canceled, false},
		{"plain error", errors.New("provider rejected the api key"), false},
		{"permission denied", syscall.EACCES, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unreachable(tc.err); got != tc.want {
				t.Fatalf("Unreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
