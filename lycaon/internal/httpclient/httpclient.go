// Package httpclient builds purpose-bound clients with phase-specific timeouts.
package httpclient

import (
	"net"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
)

const (
	// DefaultDial bounds establishing the TCP connection.
	DefaultDial = 10 * time.Second
	// DefaultTLSHandshake bounds the TLS handshake.
	DefaultTLSHandshake = 10 * time.Second

	// DiscoveryTimeout bounds provider discovery and key validation.
	DiscoveryTimeout = 20 * time.Second
	// CatalogTimeout bounds metadata fetches.
	CatalogTimeout = 15 * time.Second
)

// DefaultResponseHeader bounds time until a streaming response begins.
var DefaultResponseHeader = 60 * time.Second

// baseTransport bounds connection setup and applies host proxy selection.
func baseTransport() *http.Transport {
	cloned := http.DefaultTransport.(*http.Transport).Clone()
	cloned.Proxy = hostProxy()
	cloned.DialContext = (&net.Dialer{
		Timeout:   DefaultDial,
		KeepAlive: 30 * time.Second,
	}).DialContext
	cloned.TLSHandshakeTimeout = DefaultTLSHandshake
	return cloned
}

// Bounded returns a client with a total exchange timeout.
func Bounded(class egressclass.ID, total time.Duration) *http.Client {
	egressclass.RequireTransport(class, egressclass.HTTPBounded)
	return &http.Client{
		Transport: baseTransport(),
		Timeout:   total,
	}
}

// Streaming bounds setup and response headers without bounding the body.
func Streaming(class egressclass.ID) *http.Client {
	return StreamingWithHeaderTimeout(class, DefaultResponseHeader)
}

// StreamingWithHeaderTimeout is Streaming with a caller-chosen response-header bound.
func StreamingWithHeaderTimeout(class egressclass.ID, header time.Duration) *http.Client {
	egressclass.RequireTransport(class, egressclass.HTTPStreaming)
	tr := baseTransport()
	tr.ResponseHeaderTimeout = header
	return &http.Client{Transport: tr}
}

// Download bounds response headers and the full artifact transfer.
func Download(class egressclass.ID, total time.Duration) *http.Client {
	egressclass.RequireTransport(class, egressclass.HTTPDownload)
	tr := baseTransport()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport: tr,
		Timeout:   total,
	}
}

// WithTransport returns a copy of c using base as its transport.
func WithTransport(c *http.Client, base http.RoundTripper) *http.Client {
	if c == nil {
		return nil
	}
	out := *c
	out.Transport = base
	return &out
}
