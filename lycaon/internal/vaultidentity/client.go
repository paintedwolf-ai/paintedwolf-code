package vaultidentity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// ErrServiceAbsent: nothing listens on the socket, so the host has no
// identity service.
var ErrServiceAbsent = errors.New("vault identity service is not installed")

const requestTimeout = 30 * time.Second

// Client talks to the service over one connection, reconnecting when the
// service has gone away between requests. The service verifies a connection
// once and re-checks the caller's liveness on every request.
type Client struct {
	path string
	mu   sync.Mutex
	conn net.Conn
}

// NewClient returns a client for the socket at path.
func NewClient(path string) *Client { return &Client{path: path} }

// Do sends one request and returns the service's response. Transport
// failures are errors; refusals and other non-ok statuses are responses.
func (c *Client) Do(req Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	resp, err := c.exchange(req)
	if err != nil && c.conn == nil {
		// The earlier connection was stale; one fresh attempt.
		resp, err = c.exchange(req)
	}
	return resp, err
}

func (c *Client) exchange(req Request) (Response, error) {
	fresh := false
	if c.conn == nil {
		conn, err := dial(c.path)
		if err != nil {
			return Response{}, err
		}
		c.conn, fresh = conn, true
	}
	_ = c.conn.SetDeadline(time.Now().Add(requestTimeout))
	var resp Response
	err := WriteFrame(c.conn, req)
	if err == nil {
		err = ReadFrame(c.conn, &resp)
	}
	if err != nil {
		_ = c.conn.Close()
		c.conn = nil
		if fresh {
			return Response{}, fmt.Errorf("vault identity service: %w", err)
		}
		// A stale connection reports nothing; the caller retries once.
		return Response{}, io.ErrUnexpectedEOF
	}
	return resp, nil
}

// Close drops the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func dial(path string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "unix", path)
	if err == nil {
		return conn, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("%w: %s", ErrServiceAbsent, path)
	}
	return nil, fmt.Errorf("vault identity service: %w", err)
}
