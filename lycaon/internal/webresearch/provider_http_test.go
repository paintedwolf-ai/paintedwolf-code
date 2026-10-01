package webresearch

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRESTProviderRecoversMislabeledGzipGET(t *testing.T) {
	encodings := make(chan string, 2)
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		encoding := r.Header.Get("Accept-Encoding")
		encodings <- encoding
		if encoding != "identity" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		_, _ = io.WriteString(w, `[{"url":"https://example.com/result","title":[{"value":"Result"}]}]`)
	})
	spec := mwmblRESTSpec()
	spec.BuildURL = func(Settings, string, int) (string, error) { return srv.URL, nil }
	out := NewRESTSearchProvider(spec, KindKeyless).Search(t.Context(), Settings{}, "query", 1)
	if !out.ok || len(out.hits) != 1 || out.hits[0].URL != "https://example.com/result" {
		t.Fatalf("search result = %+v, want recovered result", out)
	}
	if len(encodings) != 2 {
		t.Fatalf("requests = %d, want two", len(encodings))
	}
	if first, second := <-encodings, <-encodings; first != "gzip" || second != "identity" {
		t.Fatalf("encodings = %q, %q, want gzip then identity", first, second)
	}
}

func TestProviderHTTPKeepsValidGzip(t *testing.T) {
	var calls atomic.Int32
	srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, "compressed result")
		_ = writer.Close()
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "create request", err)
	body, status, err := doProviderHTTP(t.Context(), req, 5, false)
	testutil.FailErr(t, "read compressed response", err)
	if string(body) != "compressed result" || status != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("body=%q status=%d requests=%d", body, status, calls.Load())
	}
}

func TestProviderHTTPDoesNotReplayWritesOrGETBodies(t *testing.T) {
	cases := []struct{ name, method, body string }{
		{"bodyless POST", http.MethodPost, ""},
		{"POST with body", http.MethodPost, "request body"},
		{"GET with body", http.MethodGet, "request body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := withMockProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = io.WriteString(w, "uncompressed response")
			})
			req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL, strings.NewReader(tc.body))
			testutil.FailErr(t, "create request", err)
			_, _, err = doProviderHTTP(t.Context(), req, 5, false)
			if !errors.Is(err, gzip.ErrHeader) || calls.Load() != 1 {
				t.Fatalf("error=%v requests=%d, want gzip header error without replay", err, calls.Load())
			}
		})
	}
}

type providerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f providerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type providerErrorBody struct {
	closed bool
	err    error
}

func (b *providerErrorBody) Read([]byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	return 0, gzip.ErrHeader
}
func (b *providerErrorBody) Close() error {
	b.closed = true
	return nil
}

func TestProviderHTTPCompressionRecoveryIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/search", nil)
	testutil.FailErr(t, "create request", err)
	req.Header.Set("X-Fixture", "preserved")
	bodies := []*providerErrorBody{{}, {}}
	calls := 0
	client := &http.Client{Transport: providerRoundTripFunc(func(sent *http.Request) (*http.Response, error) {
		if calls >= len(bodies) {
			t.Fatal("compression recovery retried more than once")
		}
		if sent.Context() != req.Context() || sent.Header.Get("X-Fixture") != "preserved" {
			t.Fatal("retry changed the request context or headers")
		}
		if calls == 1 && (!bodies[0].closed || sent.Header.Get("Accept-Encoding") != "identity") {
			t.Fatal("retry did not close the first response and request identity encoding")
		}
		body := bodies[calls]
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: body, Uncompressed: true}, nil
	})}
	_, _, err = readProviderHTTP(client, req)
	if !errors.Is(err, gzip.ErrHeader) || calls != 2 || !bodies[0].closed || !bodies[1].closed {
		t.Fatalf("error=%v requests=%d closed=%t/%t", err, calls, bodies[0].closed, bodies[1].closed)
	}
	if req.Header.Get("Accept-Encoding") != "" {
		t.Fatal("compression recovery mutated the caller's request")
	}
}

func TestProviderHTTPDoesNotRetryOtherResponseFailures(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		uncompressed bool
	}{
		{"truncated response", io.ErrUnexpectedEOF, true},
		{"checksum failure", gzip.ErrChecksum, true},
		{"caller-managed decoding", gzip.ErrHeader, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/search", nil)
			testutil.FailErr(t, "create request", err)
			body := &providerErrorBody{err: tc.err}
			calls := 0
			client := &http.Client{Transport: providerRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Body: body, Uncompressed: tc.uncompressed}, nil
			})}
			_, _, err = readProviderHTTP(client, req)
			if !errors.Is(err, tc.err) || calls != 1 || !body.closed {
				t.Fatalf("error=%v requests=%d closed=%t, want original failure without replay", err, calls, body.closed)
			}
		})
	}
}
