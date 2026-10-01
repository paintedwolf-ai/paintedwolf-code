package httpclient

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"

	"github.com/lycaon/lycaon/internal/bytebound"
)

// ErrResponseBodyTooLarge identifies a response over its byte limit.
var ErrResponseBodyTooLarge = errors.New("http response body exceeds its transport limit")

// ResponseBodyTooLargeError reports a response over its transport limit.
// ContentLength is -1 when absent.
type ResponseBodyTooLargeError struct {
	Limit         bytebound.Transport
	ContentLength int64
}

func (e *ResponseBodyTooLargeError) Error() string {
	if e.ContentLength >= 0 {
		return fmt.Sprintf("%v: declared %d bytes, limit %d", ErrResponseBodyTooLarge, e.ContentLength, e.Limit)
	}
	return fmt.Sprintf("%v: limit %d", ErrResponseBodyTooLarge, e.Limit)
}

func (e *ResponseBodyTooLargeError) Unwrap() error {
	return ErrResponseBodyTooLarge
}

// ReadResponseBody enforces a transport limit for declared and chunked bodies.
func ReadResponseBody(resp *http.Response, limit bytebound.Transport) ([]byte, error) {
	maxBytes := limit.Int64()
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("read HTTP response body: missing response body")
	}
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("read HTTP response body: invalid limit %d", maxBytes)
	}
	if resp.ContentLength > maxBytes {
		return nil, &ResponseBodyTooLargeError{Limit: limit, ContentLength: resp.ContentLength}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, &ResponseBodyTooLargeError{Limit: limit, ContentLength: resp.ContentLength}
	}
	return body, nil
}
