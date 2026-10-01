package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/bytebound"
)

const feedErrorExcerptBytes = 4096

// FeedStatusError retains the status and a bounded diagnostic excerpt.
type FeedStatusError struct {
	StatusCode int
	Excerpt    string
	Err        error
}

func (e *FeedStatusError) Error() string {
	message := fmt.Sprintf("feed HTTP %d", e.StatusCode)
	if e.Excerpt != "" {
		message += ": " + e.Excerpt
	}
	if e.Err != nil {
		message += ": read error body: " + e.Err.Error()
	}
	return message
}

func (e *FeedStatusError) Unwrap() error { return e.Err }

func readFeedResponse(resp *http.Response, limit bytebound.Transport) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, feedErrorExcerptBytes))
		return nil, &FeedStatusError{
			StatusCode: resp.StatusCode,
			Excerpt:    strings.TrimSpace(string(body)),
			Err:        err,
		}
	}
	body, err := ReadResponseBody(resp, limit)
	if err != nil {
		return nil, fmt.Errorf("read feed body: %w", err)
	}
	return body, nil
}
