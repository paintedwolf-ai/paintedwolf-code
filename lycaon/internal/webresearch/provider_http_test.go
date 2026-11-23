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

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRESTProviderRecoversMislabeledGzipGET(t *testing.T) {
	encodings := make(chan string, 2)
	srv := withProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		encoding := r.Header.Get("Accept-Encoding")
		encodings <- encoding
		if encoding != "identity" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		_, _ = io.WriteString(w, `[{"url":"https://example.com/result","title":[{"value":"Result"}]}]`)
	})
	spec := mwmblRESTSpec()
	spec.BuildURL = func(Settings, string, int) (string, error) { return srv.URL, nil }
	out := NewRESTSearchProvider(spec, KindKeyless, srv.exchange).Search(t.Context(), Settings{}, "query", 1)
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
	srv := withProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, "compressed result")
		_ = writer.Close()
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "create request", err)
	body, status, err := doProviderHTTP(t.Context(), req, 5, false, srv.exchange)
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
			srv := withProviderHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = io.WriteString(w, "uncompressed response")
			})
			req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL, strings.NewReader(tc.body))
			testutil.FailErr(t, "create request", err)
			_, _, err = doProviderHTTP(t.Context(), req, 5, false, srv.exchange)
			if tc.method == http.MethodGet && tc.body != "" {
				if err == nil || calls.Load() != 0 {
					t.Fatalf("GET body error=%v requests=%d", err, calls.Load())
				}
				return
			}
			if !errors.Is(err, gzip.ErrHeader) || calls.Load() != 1 {
				t.Fatalf("error=%v requests=%d, want gzip header error without replay", err, calls.Load())
			}
		})
	}
}

func TestProviderHTTPCompressionRecoveryIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/search", nil)
	testutil.FailErr(t, "create request", err)
	req.Header.Set("X-Fixture", "preserved")
	calls := 0
	firstTimeout := time.Duration(0)
	exchange := func(sentContext context.Context, sent outboundhttp.Request) (outboundhttp.Response, error) {
		if sentContext != ctx || responseHeader(sent.OriginHeaders, "X-Fixture") != "preserved" {
			t.Fatal("retry changed context or headers")
		}
		if calls == 0 {
			firstTimeout = sent.Timeout
		} else if calls > 1 || sent.Timeout > firstTimeout || responseHeader(sent.OriginHeaders, "Accept-Encoding") != "identity" {
			t.Fatal("compression recovery exceeded retry or deadline bounds")
		}
		calls++
		return outboundhttp.Response{}, gzip.ErrHeader
	}
	_, _, err = doProviderHTTP(ctx, req, 30, false, exchange)
	if !errors.Is(err, gzip.ErrHeader) || calls != 2 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	if req.Header.Get("Accept-Encoding") != "" {
		t.Fatal("retry mutated caller headers")
	}
}

func TestProviderHTTPDoesNotRetryOtherResponseFailures(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, gzip.ErrChecksum, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/search", nil)
			testutil.FailErr(t, "create request", err)
			calls := 0
			exchange := func(context.Context, outboundhttp.Request) (outboundhttp.Response, error) {
				calls++
				return outboundhttp.Response{}, failure
			}
			_, _, err = doProviderHTTP(t.Context(), req, 5, false, exchange)
			if !errors.Is(err, failure) || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestProviderHTTPPreservesCallerManagedEncoding(t *testing.T) {
	calls := 0
	srv := withProviderHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(w, "caller-decoded bytes")
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	testutil.FailErr(t, "create request", err)
	req.Header.Set("Accept-Encoding", "gzip")
	body, _, err := doProviderHTTP(t.Context(), req, 5, false, srv.exchange)
	testutil.FailErr(t, "read caller encoded bytes", err)
	if string(body) != "caller-decoded bytes" || calls != 1 {
		t.Fatalf("body=%q calls=%d", body, calls)
	}
}
