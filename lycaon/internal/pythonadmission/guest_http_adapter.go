package pythonadmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrStreamHandleNotFound is returned when a guest references an unknown or
	// closed HTTP stream handle.
	ErrStreamHandleNotFound = errors.New("guest HTTP stream handle not found")
)

// GuestHTTPRequest is the JSON wire request sent by the guest Python
// `requests.adapters.BaseAdapter` shim across the host bridge.
type GuestHTTPRequest struct {
	OperationID   string                       `json:"operation_id,omitempty"`
	Method        string                       `json:"method"`
	URL           string                       `json:"url"`
	Params        map[string]string            `json:"params,omitempty"`
	Headers       map[string]string            `json:"headers,omitempty"`
	JSONBody      any                          `json:"json_body,omitempty"`
	BodyBase64    string                       `json:"body_base64,omitempty"`
	TimeoutMillis int64                        `json:"timeout_ms,omitempty"`
	Stream        bool                         `json:"stream,omitempty"`
	DeclaredFrame *pythonruntime.DeclaredFrame `json:"declared_frame,omitempty"`
}

// GuestHTTPResponse is the JSON wire response returned to the guest Python
// `requests.adapters.BaseAdapter` shim.
type GuestHTTPResponse struct {
	StatusCode     int                    `json:"status_code,omitempty"`
	Headers        map[string]string      `json:"headers,omitempty"`
	FinalURL       string                 `json:"final_url,omitempty"`
	Redirects      []HTTPRedirectHop      `json:"redirects,omitempty"`
	BodyBase64     string                 `json:"body_base64,omitempty"`
	StreamHandleID string                 `json:"stream_handle_id,omitempty"`
	BytesReceived  int64                  `json:"bytes_received,omitempty"`
	PartialFailure bool                   `json:"partial_failure,omitempty"`
	StopCode       pythonruntime.StopCode `json:"stop_code,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

// GuestHTTPStreamChunk is returned by GuestHTTPAdapter.ReadStreamChunk for
// streaming `requests.Session` responses.
type GuestHTTPStreamChunk struct {
	DataBase64     string `json:"data_base64,omitempty"`
	EOF            bool   `json:"eof,omitempty"`
	PartialFailure bool   `json:"partial_failure,omitempty"`
	Error          string `json:"error,omitempty"`
}

type guestStreamHandle struct {
	body   io.ReadCloser
	record *HTTPRecord
}

// GuestHTTPAdapter bridges guest Python `requests.adapters.BaseAdapter` JSON
// requests into the host-mediated HTTPTransport.
type GuestHTTPAdapter struct {
	transport *HTTPTransport
	seq       atomic.Uint64
	mu        sync.Mutex
	streams   map[string]*guestStreamHandle
}

// NewGuestHTTPAdapter constructs a GuestHTTPAdapter backed by transport.
func NewGuestHTTPAdapter(transport *HTTPTransport) *GuestHTTPAdapter {
	return &GuestHTTPAdapter{
		transport: transport,
		streams:   make(map[string]*guestStreamHandle),
	}
}

// HandleJSON decodes a GuestHTTPRequest JSON payload, executes it through
// HTTPTransport.Do, and returns a JSON-encoded GuestHTTPResponse.
func (a *GuestHTTPAdapter) HandleJSON(ctx context.Context, rawReq []byte) ([]byte, error) {
	if a.transport == nil {
		return nil, errors.New("guest HTTP adapter missing HTTPTransport")
	}
	var greq GuestHTTPRequest
	if err := json.Unmarshal(rawReq, &greq); err != nil {
		return nil, fmt.Errorf("decode guest HTTP request: %w", err)
	}

	req := HTTPRequest{
		OperationID:   greq.OperationID,
		Method:        greq.Method,
		URL:           greq.URL,
		Params:        greq.Params,
		Headers:       greq.Headers,
		JSONBody:      greq.JSONBody,
		Stream:        greq.Stream,
		DeclaredFrame: greq.DeclaredFrame,
	}
	if greq.TimeoutMillis > 0 {
		req.Timeout = time.Duration(greq.TimeoutMillis) * time.Millisecond
	}
	if greq.BodyBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(greq.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode guest HTTP request body_base64: %w", err)
		}
		req.RawBody = bytes.NewReader(decoded)
	}

	resp, err := a.transport.Do(ctx, req)
	if err != nil {
		gresp := GuestHTTPResponse{
			Error: err.Error(),
		}
		var admErr *HTTPAdmissionError
		if errors.As(err, &admErr) {
			if admErr.Decision.Stop != nil {
				gresp.StopCode = admErr.Decision.Stop.Code
			}
			if admErr.Record != nil {
				gresp.Redirects = admErr.Record.Redirects
				gresp.PartialFailure = admErr.Record.PartialFailure
				gresp.BytesReceived = admErr.Record.BytesReceived
			}
		} else if resp != nil && resp.Record != nil {
			gresp.StatusCode = resp.StatusCode
			gresp.FinalURL = resp.FinalURL
			gresp.Redirects = resp.Redirects
			gresp.PartialFailure = resp.Record.PartialFailure
			gresp.BytesReceived = resp.Record.BytesReceived
		}
		out, _ := json.Marshal(gresp)
		return out, err
	}

	flatHeaders := make(map[string]string, len(resp.Headers))
	for k, vals := range resp.Headers {
		if len(vals) > 0 {
			flatHeaders[k] = vals[0]
		}
	}

	gresp := GuestHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    flatHeaders,
		FinalURL:   resp.FinalURL,
		Redirects:  resp.Redirects,
	}

	if greq.Stream {
		handleID := fmt.Sprintf("http-stream-%d", a.seq.Add(1))
		a.mu.Lock()
		a.streams[handleID] = &guestStreamHandle{
			body:   resp.Body,
			record: resp.Record,
		}
		a.mu.Unlock()
		gresp.StreamHandleID = handleID
	} else {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		gresp.BodyBase64 = base64.StdEncoding.EncodeToString(bodyBytes)
		if resp.Record != nil {
			gresp.BytesReceived = resp.Record.BytesReceived
			gresp.PartialFailure = resp.Record.PartialFailure
		}
		if readErr != nil {
			gresp.PartialFailure = true
			gresp.Error = readErr.Error()
		}
	}

	out, err := json.Marshal(gresp)
	if err != nil {
		return nil, fmt.Errorf("encode guest HTTP response: %w", err)
	}
	return out, nil
}

// ReadStreamChunk reads up to maxBytes from an open streaming response handle.
// If ctx is cancelled mid-stream, the underlying body is closed immediately
// and PartialFailure is recorded truthfully.
func (a *GuestHTTPAdapter) ReadStreamChunk(ctx context.Context, handleID string, maxBytes int) ([]byte, error) {
	a.mu.Lock()
	handle, ok := a.streams[handleID]
	a.mu.Unlock()
	if !ok {
		return nil, ErrStreamHandleNotFound
	}
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	if err := ctx.Err(); err != nil {
		if tb, ok := handle.body.(*trackedHTTPBody); ok {
			tb.MarkCancelled(err)
		}
		_ = a.CloseStream(handleID)
		chunk := GuestHTTPStreamChunk{
			PartialFailure: true,
			Error:          err.Error(),
		}
		out, _ := json.Marshal(chunk)
		return out, err
	}

	buf := make([]byte, maxBytes)
	n, readErr := handle.body.Read(buf)
	chunk := GuestHTTPStreamChunk{}
	if n > 0 {
		chunk.DataBase64 = base64.StdEncoding.EncodeToString(buf[:n])
	}
	if errors.Is(readErr, io.EOF) {
		chunk.EOF = true
		_ = a.CloseStream(handleID)
		out, err := json.Marshal(chunk)
		return out, err
	}
	if readErr != nil {
		chunk.PartialFailure = true
		chunk.Error = readErr.Error()
		_ = a.CloseStream(handleID)
		out, _ := json.Marshal(chunk)
		return out, readErr
	}
	out, err := json.Marshal(chunk)
	return out, err
}

// CloseStream closes and removes a streaming response handle.
func (a *GuestHTTPAdapter) CloseStream(handleID string) error {
	a.mu.Lock()
	handle, ok := a.streams[handleID]
	if ok {
		delete(a.streams, handleID)
	}
	a.mu.Unlock()
	if !ok {
		return nil
	}
	return handle.body.Close()
}
