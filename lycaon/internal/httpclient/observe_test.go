package httpclient

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Cleanup releases unanswered handlers before closing the server.
func silentServer(t *testing.T, http2 bool) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	if http2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

func TestObserveSeesDeliveredRequestWithNoAnswerOverHTTP2(t *testing.T) {
	srv := silentServer(t, true)

	tr, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("test server client does not use *http.Transport")
	}
	tr = tr.Clone()
	tr.ForceAttemptHTTP2 = true
	tr.ResponseHeaderTimeout = 300 * time.Millisecond

	ctx, read := Observe(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("want a response-header timeout, got a response")
	}

	obs := read()
	if !obs.Delivered() {
		t.Fatalf("want the request recorded as delivered, got %+v", obs)
	}
	if obs.FirstByte {
		t.Fatalf("no response byte can have arrived on a header timeout: %+v", obs)
	}
	// A response-header timeout can follow a delivered request.
	if !Unreachable(err) {
		t.Fatalf("expected the error to read as unreachable, which is exactly why "+
			"the observation is needed to tell it from a real one: %v", err)
	}
}

func TestObserveSeesRequestThatNeverReachedTheServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("release port: %v", closeErr)
	}

	ctx, read := Observe(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("want a dial failure, got a response")
	}

	obs := read()
	if obs.Delivered() {
		t.Fatalf("a refused dial must not read as delivered: %+v", obs)
	}
	if obs.WroteRequest {
		t.Fatalf("a refused dial cannot have written a request: %+v", obs)
	}
}

func TestObserveSeesACompletedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, read := Observe(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if closeErr := resp.Body.Close(); closeErr != nil {
		t.Fatalf("close body: %v", closeErr)
	}

	obs := read()
	if !obs.Delivered() || !obs.FirstByte {
		t.Fatalf("a served response is delivered and answered: %+v", obs)
	}
}
