package httpclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadResponseBodyAcceptsBodyAtTransportLimit(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("12345")),
		ContentLength: 5,
	}

	body, err := ReadResponseBody(resp, bytebound.Transport(5))
	testutil.FailErr(t, "read bounded response", err)
	if string(body) != "12345" {
		t.Fatalf("body = %q, want exact response", body)
	}
}

func TestReadResponseBodyRejectsDeclaredOversizeWithoutReading(t *testing.T) {
	reader := &countingReader{Reader: strings.NewReader("123456")}
	resp := &http.Response{
		Body:          io.NopCloser(reader),
		ContentLength: 6,
	}

	_, err := ReadResponseBody(resp, bytebound.Transport(5))
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want response-body limit", err)
	}
	if reader.reads != 0 {
		t.Fatalf("body reads = %d, want early Content-Length rejection", reader.reads)
	}
}

func TestReadResponseBodyRejectsChunkedOversize(t *testing.T) {
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader("123456")),
		ContentLength: -1,
	}

	_, err := ReadResponseBody(resp, bytebound.Transport(5))
	if !errors.Is(err, ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want response-body limit", err)
	}
	var limitErr *ResponseBodyTooLargeError
	if !errors.As(err, &limitErr) || limitErr.ContentLength != -1 {
		t.Fatalf("typed error = %#v, want unknown content length", limitErr)
	}
}

type countingReader struct {
	io.Reader
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}
