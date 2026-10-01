package outboundhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPermanentSeparatesFutileFailuresFromRetryableOnes(t *testing.T) {
	permanent := []error{
		&BodyTooLargeError{Limit: 10},
		&RequestInvalidError{Reason: "unsupported http method"},
		&RedirectError{Reason: "too many redirects"},
		fmt.Errorf("wrapped: %w", &BodyTooLargeError{Limit: 10}),
	}
	for _, err := range permanent {
		if !Permanent(err) {
			t.Fatalf("%v was reported retryable", err)
		}
	}
	retryable := []error{
		context.DeadlineExceeded,
		io.ErrUnexpectedEOF,
		errors.New("dial tcp: connection refused"),
		&TransferError{Err: context.DeadlineExceeded},
	}
	for _, err := range retryable {
		if Permanent(err) {
			t.Fatalf("%v was reported permanent", err)
		}
	}
}

func TestUnusableDeclarationsArePermanent(t *testing.T) {
	cases := []Request{
		{Method: "TRACE", URL: "https://example.test"},
		{Method: http.MethodGet, URL: "https://example.test", Body: []byte("x")},
		{Method: http.MethodGet, URL: "ftp://example.test"},
		{Method: http.MethodGet, URL: "https://user:pass@example.test"},
		{Method: http.MethodGet, URL: "https://example.test", Redirects: "follow-everything"},
		{Method: http.MethodGet, URL: "https://example.test", OriginHeaders: []Header{{Name: "Host", Value: "x"}}},
	}
	for _, request := range cases {
		_, err := Do(context.Background(), request)
		if err == nil || !Permanent(err) {
			t.Fatalf("%+v: error = %v, want a permanent fault", request, err)
		}
	}
}

func TestBodyTooLargeIsPermanentThroughDo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 64))
	}))
	t.Cleanup(server.Close)
	_, err := Do(context.Background(), Request{
		Method: http.MethodGet, URL: server.URL, MaxBodyBytes: 8, AllowAddress: allowLoopback,
	})
	var oversized *BodyTooLargeError
	if !errors.As(err, &oversized) || !Permanent(err) {
		t.Fatalf("error = %v, want a permanent body-too-large fault", err)
	}
}

func TestStreamAttributesATransferFailureToTheTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 8))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Closing early leaves the declared body unfinished.
		if hijacker, ok := w.(http.Hijacker); ok {
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	t.Cleanup(server.Close)

	sinkFailure := errors.New("write dest: disk is on fire")
	_, err := Do(context.Background(), Request{
		Method: http.MethodGet, URL: server.URL, AllowAddress: allowLoopback,
		StreamBody: func(body io.Reader) error {
			if _, readErr := io.Copy(io.Discard, body); readErr != nil {
				return fmt.Errorf("write dest: %w", readErr)
			}
			return sinkFailure
		},
	})
	var transfer *TransferError
	switch {
	case errors.As(err, &transfer):
		if errors.Is(err, sinkFailure) {
			t.Fatal("a transfer failure was reported as the sink's own failure")
		}
	case err == nil:
		t.Skip("the server completed the body before the connection dropped")
	default:
		t.Fatalf("error = %v, want a TransferError", err)
	}
}

func TestStreamKeepsAGenuineSinkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("complete body"))
	}))
	t.Cleanup(server.Close)
	sinkFailure := errors.New("write dest: no space left on device")
	_, err := Do(context.Background(), Request{
		Method: http.MethodGet, URL: server.URL, AllowAddress: allowLoopback,
		StreamBody: func(body io.Reader) error {
			_, _ = io.Copy(io.Discard, body)
			return sinkFailure
		},
	})
	if !errors.Is(err, sinkFailure) {
		t.Fatalf("error = %v, want the sink's own failure", err)
	}
}
