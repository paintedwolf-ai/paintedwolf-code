package httpclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type feedFailReader struct{ err error }

func (r feedFailReader) Read([]byte) (int, error) { return 0, r.err }

func TestFeedResponseBoundsAndCleanup(t *testing.T) {
	failure := errors.New("body transfer failed")
	for _, tc := range []struct {
		name        string
		status      int
		reader      io.Reader
		length      int64
		want        string
		oversized   bool
		statusError bool
		cause       error
	}{
		{name: "below limit", status: 200, reader: strings.NewReader("{}"), length: 2, want: "{}"},
		{name: "exact limit", status: 200, reader: strings.NewReader("{}              "), length: 16, want: "{}              "},
		{name: "unknown exact length", status: 200, reader: strings.NewReader("{}              "), length: -1, want: "{}              "},
		{name: "announced overflow", status: 200, reader: feedFailReader{err: failure}, length: 17, oversized: true},
		{name: "unannounced overflow", status: 200, reader: strings.NewReader("{}               "), length: -1, oversized: true},
		{name: "underreported length", status: 200, reader: strings.NewReader("{}               "), length: 2, oversized: true},
		{name: "read failure", status: 200, reader: io.MultiReader(strings.NewReader("{"), feedFailReader{err: failure}), cause: failure},
		{name: "non success", status: 503, reader: strings.NewReader(strings.Repeat("x", 8192)), length: 8192, statusError: true},
		{name: "no content", status: 204, reader: strings.NewReader(""), statusError: true},
		{name: "error body failure", status: 500, reader: feedFailReader{err: failure}, statusError: true, cause: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &feedTestBody{Reader: tc.reader}
			tr := &feedTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: b, ContentLength: tc.length}, nil
			}}
			body, err := getFeed(t.Context(), "https://8.8.8.8/feed", feedTestOptions(), feedTestNetwork(tr))
			var large *ResponseBodyTooLargeError
			var status *FeedStatusError
			if errors.As(err, &large) != tc.oversized || errors.As(err, &status) != tc.statusError || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if errors.Is(err, ErrResponseBodyTooLarge) != tc.oversized {
				t.Fatalf("overflow classification lost: %v", err)
			}
			if large != nil && large.Limit != 16 {
				t.Fatalf("overflow limit=%d", large.Limit)
			}
			if status != nil && (status.StatusCode != tc.status || len(status.Excerpt) > 4096) {
				t.Fatalf("status=%+v", status)
			}
			if tc.status == 503 && len(status.Excerpt) != 4096 {
				t.Fatalf("excerpt length=%d", len(status.Excerpt))
			}
			if string(body) != tc.want || (!tc.oversized && !tc.statusError && tc.cause == nil && err != nil) {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if !b.closed || tr.closed != 1 {
				t.Fatalf("resources not closed: %v/%d", b.closed, tr.closed)
			}
		})
	}
}
