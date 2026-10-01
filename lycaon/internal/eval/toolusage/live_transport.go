package toolusage

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

func newLiveClient(base, token string) *liveClient {
	// Synchronous mutations may include scans, worktrees, and worker preparation.
	// Their request context owns cancellation; elapsed HTTP time is not failure.
	return &liveClient{base: base, token: token, http: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func evidenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return context.WithCancel(ctx)
	}
	// After cancellation, allow a separate grace period to stop work and retain evidence.
	return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
}

func taskContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

type liveHTTPError struct {
	Status int
	Body   string
}

func (e *liveHTTPError) Error() string { return fmt.Sprintf("sidecar HTTP %d: %s", e.Status, e.Body) }

func (c *liveClient) do(req *http.Request) (*http.Response, error) {
	return c.request(req, req.Method == http.MethodGet || req.Method == http.MethodHead)
}

// Mutation replay is opt-in at a caller with a durable operation identity.
func (c *liveClient) request(req *http.Request, replayable bool) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		current := req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			current.Body = body
		}
		resp, err := c.http.Do(current) // #nosec G704 -- The evaluation operator explicitly selects the sidecar destination.
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				err = readErr
			} else if resp.StatusCode < http.StatusBadRequest {
				resp.Body = io.NopCloser(bytes.NewReader(body))
				return resp, nil
			} else {
				err = &liveHTTPError{Status: resp.StatusCode, Body: string(body)}
			}
		}
		if !replayable || !retryableLiveError(err) || req.Context().Err() != nil {
			return nil, err
		}
		delay := liveRetryDelay(attempt, resp)
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
	}
}

func retryableLiveError(err error) bool {
	var status *liveHTTPError
	if errors.As(err, &status) {
		switch status.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var certificate x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &certificate) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return false
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func liveRetryDelay(attempt int, resp *http.Response) time.Duration {
	delay := time.Second << min(attempt, 5)
	if resp != nil {
		value := resp.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if when, err := http.ParseTime(value); err == nil {
			delay = max(delay, time.Until(when))
		}
	}
	return delay
}
