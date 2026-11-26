package pythonadmission

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

var (
	// ErrTLSRequired is returned when a non-HTTPS URL is requested under TLS enforcement.
	ErrTLSRequired = errors.New("TLS (https) is required for HTTP transport")
	// ErrCredentialRecipientDenied is returned when credential-recipient screening blocks a request.
	ErrCredentialRecipientDenied = errors.New("credential-recipient policy denied outgoing headers")
	// ErrTooManyRedirects is returned when the redirect hop cap is exceeded.
	ErrTooManyRedirects = errors.New("maximum redirect count exceeded")
)

// HTTPAdmissionError wraps a refused initial or redirected HTTP admission.
type HTTPAdmissionError struct {
	URL      string
	Redirect bool
	Decision pythonruntime.OperationDecision
	Record   *HTTPRecord
}

func (e *HTTPAdmissionError) Error() string {
	kind := "request"
	if e.Redirect {
		kind = "redirect"
	}
	if e.Decision.Stop != nil {
		return fmt.Sprintf("http %s to %s blocked (%s): %s", kind, e.URL, e.Decision.Stop.Code, e.Decision.Reason)
	}
	return fmt.Sprintf("http %s to %s denied (%s): %s", kind, e.URL, e.Decision.Kind, e.Decision.Reason)
}

// CredentialScreener validates whether outgoing sensitive headers (such as
// Authorization or Cookie) may be delivered to targetURL.
type CredentialScreener func(targetURL *url.URL, headers http.Header) error

// HTTPTransportConfig configures the host-mediated synchronous HTTP transport
// backing requests.Session.
type HTTPTransportConfig struct {
	ExecutionID        string
	CallID             string
	Observer           pythonruntime.OperationObserver
	Client             *http.Client
	AllowPlainHTTP     bool
	AllowLoopbackHTTP  bool
	CredentialScreener CredentialScreener
	MaxRedirects       int
	DefaultTimeout     time.Duration
	Clock              pythonruntime.Clock
	Evidence           EvidenceSink
}

// HTTPRequest describes one synchronous requests.Session request.
type HTTPRequest struct {
	OperationID   string
	Method        string
	URL           string
	Params        map[string]string
	Headers       map[string]string
	JSONBody      any
	RawBody       io.Reader
	Timeout       time.Duration
	Stream        bool
	DeclaredFrame *pythonruntime.DeclaredFrame
}

// HTTPResponse carries the mediated response, redirect chain, and evidence record.
type HTTPResponse struct {
	StatusCode int
	Headers    http.Header
	FinalURL   string
	Redirects  []HTTPRedirectHop
	Body       io.ReadCloser
	Record     *HTTPRecord
}

// HTTPTransport mediates synchronous HTTP requests for requests.Session through
// OperationObserver, enforcing TLS, credential screening, redirect re-admission,
// streaming byte accounting, and partial-failure truth.
type HTTPTransport struct {
	cfg      HTTPTransportConfig
	base     *http.Client
	clock    pythonruntime.Clock
	evidence EvidenceSink
	seq      atomic.Uint64
}

// NewHTTPTransport constructs an HTTPTransport with TLS 1.2+ defaults.
func NewHTTPTransport(cfg HTTPTransportConfig) *HTTPTransport {
	base := cfg.Client
	if base == nil {
		base = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			},
		}
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = 10
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	ev := cfg.Evidence
	if ev == nil {
		ev = NewMemoryEvidenceStore()
	}
	return &HTTPTransport{
		cfg:      cfg,
		base:     base,
		clock:    clk,
		evidence: ev,
	}
}

// Do executes one mediated HTTP request. Redirects re-enter
// OperationObserver.Admit(EffectHTTP) at every destination URL.
func (t *HTTPTransport) Do(ctx context.Context, req HTTPRequest) (*HTTPResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	opID := req.OperationID
	if opID == "" {
		opID = fmt.Sprintf("http-%s-%d", t.cfg.ExecutionID, t.seq.Add(1))
	}

	parsedURL, err := buildRequestURL(req.URL, req.Params)
	if err != nil {
		return nil, fmt.Errorf("invalid request url: %w", err)
	}

	if err := t.checkTLS(parsedURL); err != nil {
		return nil, err
	}

	bodyReader, bytesSent, contentType, err := prepareRequestBody(req)
	if err != nil {
		return nil, err
	}

	headers := make(http.Header)
	for k, v := range req.Headers {
		headers.Set(k, v)
	}
	if contentType != "" && headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", contentType)
	}

	if err := t.screenCredentials(parsedURL, headers); err != nil {
		return nil, err
	}

	// Admit initial request before opening any network connection.
	if t.cfg.Observer != nil {
		dec, admitErr := t.cfg.Observer.Admit(ctx, pythonruntime.ReachedOperation{
			OperationID: opID,
			ExecutionID: t.cfg.ExecutionID,
			Family:      pythonruntime.EffectHTTP,
			Operation:   "http.request",
			Target: pythonruntime.CanonicalTarget{
				Namespace: pythonruntime.Namespace("http"),
				RootID:    parsedURL.Host,
				RelPath:   parsedURL.RequestURI(),
			},
			Args: map[string]any{
				"method": method,
				"url":    parsedURL.String(),
			},
			DeclaredFrame: req.DeclaredFrame,
		})
		if admitErr != nil {
			return nil, admitErr
		}
		if dec.Kind != pythonruntime.DecisionAllow {
			rec := &HTTPRecord{
				ExecutionID: t.cfg.ExecutionID,
				OperationID: opID,
				CallID:      t.cfg.CallID,
				Method:      method,
				URL:         parsedURL.String(),
				Error:       dec.Reason,
				RecordedAt:  t.clock.Now().UTC(),
			}
			t.evidence.RecordHTTP(*rec)
			return nil, &HTTPAdmissionError{
				URL:      parsedURL.String(),
				Decision: dec,
				Record:   rec,
			}
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = t.cfg.DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)

	httpReq, err := http.NewRequestWithContext(reqCtx, method, parsedURL.String(), bodyReader)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header = headers

	var (
		redirectsMu sync.Mutex
		redirects   []HTTPRedirectHop
		redirectErr error
	)

	clientCopy := *t.base
	clientCopy.CheckRedirect = func(nextReq *http.Request, via []*http.Request) error {
		fromURL := ""
		statusCode := 0
		if len(via) > 0 {
			prev := via[len(via)-1]
			fromURL = prev.URL.String()
			if nextReq.Response != nil {
				statusCode = nextReq.Response.StatusCode
			}
		}
		if len(via) >= t.cfg.MaxRedirects {
			return ErrTooManyRedirects
		}
		if err := t.checkTLS(nextReq.URL); err != nil {
			redirectsMu.Lock()
			redirects = append(redirects, HTTPRedirectHop{
				FromURL:    fromURL,
				ToURL:      nextReq.URL.String(),
				StatusCode: statusCode,
				Admitted:   false,
				Reason:     err.Error(),
			})
			redirectsMu.Unlock()
			return err
		}
		// Strip sensitive headers on cross-origin redirect before screening.
		if len(via) > 0 && !sameOrigin(via[0].URL, nextReq.URL) {
			nextReq.Header.Del("Authorization")
			nextReq.Header.Del("Cookie")
		}
		if err := t.screenCredentials(nextReq.URL, nextReq.Header); err != nil {
			redirectsMu.Lock()
			redirects = append(redirects, HTTPRedirectHop{
				FromURL:    fromURL,
				ToURL:      nextReq.URL.String(),
				StatusCode: statusCode,
				Admitted:   false,
				Reason:     err.Error(),
			})
			redirectsMu.Unlock()
			return err
		}
		if t.cfg.Observer != nil {
			redirOpID := fmt.Sprintf("%s-redirect-%d", opID, len(via))
			dec, admitErr := t.cfg.Observer.Admit(nextReq.Context(), pythonruntime.ReachedOperation{
				OperationID: redirOpID,
				ExecutionID: t.cfg.ExecutionID,
				Family:      pythonruntime.EffectHTTP,
				Operation:   "http.redirect",
				Target: pythonruntime.CanonicalTarget{
					Namespace: pythonruntime.Namespace("http"),
					RootID:    nextReq.URL.Host,
					RelPath:   nextReq.URL.RequestURI(),
				},
				Args: map[string]any{
					"method":   nextReq.Method,
					"from_url": fromURL,
					"to_url":   nextReq.URL.String(),
					"hop":      len(via),
				},
				DeclaredFrame: req.DeclaredFrame,
			})
			if admitErr != nil {
				return admitErr
			}
			hop := HTTPRedirectHop{
				FromURL:    fromURL,
				ToURL:      nextReq.URL.String(),
				StatusCode: statusCode,
				Admitted:   dec.Kind == pythonruntime.DecisionAllow,
				Reason:     dec.Reason,
			}
			if dec.Stop != nil {
				hop.StopCode = dec.Stop.Code
			}
			redirectsMu.Lock()
			redirects = append(redirects, hop)
			redirectsMu.Unlock()

			if dec.Kind != pythonruntime.DecisionAllow {
				redirectErr = &HTTPAdmissionError{
					URL:      nextReq.URL.String(),
					Redirect: true,
					Decision: dec,
				}
				return redirectErr
			}
			return nil
		}
		redirectsMu.Lock()
		redirects = append(redirects, HTTPRedirectHop{
			FromURL:    fromURL,
			ToURL:      nextReq.URL.String(),
			StatusCode: statusCode,
			Admitted:   true,
		})
		redirectsMu.Unlock()
		return nil
	}

	resp, doErr := clientCopy.Do(httpReq)
	if doErr != nil {
		cancel()
		redirectsMu.Lock()
		copiedRedirects := append([]HTTPRedirectHop(nil), redirects...)
		redirectsMu.Unlock()

		partial := len(copiedRedirects) > 0 || bytesSent > 0
		rec := &HTTPRecord{
			ExecutionID:    t.cfg.ExecutionID,
			OperationID:    opID,
			CallID:         t.cfg.CallID,
			Method:         method,
			URL:            parsedURL.String(),
			Redirects:      copiedRedirects,
			BytesSent:      bytesSent,
			PartialFailure: partial,
			Error:          doErr.Error(),
			RecordedAt:     t.clock.Now().UTC(),
		}
		t.evidence.RecordHTTP(*rec)
		if t.cfg.Observer != nil {
			t.cfg.Observer.RecordOutcome(ctx, opID, pythonruntime.OperationOutcome{
				Succeeded:        false,
				BytesTransferred: bytesSent,
				Error:            doErr.Error(),
			})
		}
		var admErr *HTTPAdmissionError
		if errors.As(redirectErr, &admErr) {
			admErr.Record = rec
			return nil, admErr
		}
		return nil, doErr
	}

	redirectsMu.Lock()
	copiedRedirects := append([]HTTPRedirectHop(nil), redirects...)
	redirectsMu.Unlock()

	rec := &HTTPRecord{
		ExecutionID: t.cfg.ExecutionID,
		OperationID: opID,
		CallID:      t.cfg.CallID,
		Method:      method,
		URL:         parsedURL.String(),
		FinalURL:    resp.Request.URL.String(),
		Redirects:   copiedRedirects,
		Status:      resp.StatusCode,
		BytesSent:   bytesSent,
		RecordedAt:  t.clock.Now().UTC(),
	}

	tracked := &trackedHTTPBody{
		rc:       resp.Body,
		cancel:   cancel,
		ctx:      ctx,
		opID:     opID,
		rec:      rec,
		observer: t.cfg.Observer,
		evidence: t.evidence,
	}

	if req.Stream {
		return &HTTPResponse{
			StatusCode: resp.StatusCode,
			Headers:    resp.Header.Clone(),
			FinalURL:   resp.Request.URL.String(),
			Redirects:  copiedRedirects,
			Body:       tracked,
			Record:     rec,
		}, nil
	}

	data, readErr := io.ReadAll(tracked)
	_ = tracked.Close()
	httpResp := &HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		FinalURL:   resp.Request.URL.String(),
		Redirects:  copiedRedirects,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Record:     rec,
	}
	if readErr != nil {
		return httpResp, readErr
	}
	return httpResp, nil
}

func (t *HTTPTransport) checkTLS(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" {
		return nil
	}
	if scheme != "http" {
		return fmt.Errorf("%w: unsupported scheme %q", ErrTLSRequired, u.Scheme)
	}
	if t.cfg.AllowPlainHTTP {
		return nil
	}
	if t.cfg.AllowLoopbackHTTP && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrTLSRequired, u.String())
}

func (t *HTTPTransport) screenCredentials(u *url.URL, headers http.Header) error {
	if t.cfg.CredentialScreener == nil {
		return nil
	}
	if err := t.cfg.CredentialScreener(u, headers); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialRecipientDenied, err)
	}
	return nil
}

type trackedHTTPBody struct {
	rc       io.ReadCloser
	cancel   context.CancelFunc
	ctx      context.Context
	opID     string
	rec      *HTTPRecord
	observer pythonruntime.OperationObserver
	evidence EvidenceSink

	mu        sync.Mutex
	received  int64
	readErr   error
	finalized bool
}

func (b *trackedHTTPBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	b.received += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		b.readErr = err
	}
	b.mu.Unlock()
	if err != nil {
		b.finalize()
	}
	return n, err
}

func (b *trackedHTTPBody) Close() error {
	err := b.rc.Close()
	b.finalize()
	return err
}

func (b *trackedHTTPBody) finalize() {
	b.mu.Lock()
	if b.finalized {
		b.mu.Unlock()
		return
	}
	b.finalized = true
	b.rec.BytesReceived = b.received
	if b.readErr != nil {
		b.rec.PartialFailure = true
		b.rec.Error = b.readErr.Error()
	}
	recCopy := *b.rec
	succeeded := b.readErr == nil
	totalBytes := b.rec.BytesSent + b.received
	errMsg := b.rec.Error
	b.mu.Unlock()

	if b.cancel != nil {
		b.cancel()
	}
	b.evidence.RecordHTTP(recCopy)
	if b.observer != nil {
		b.observer.RecordOutcome(b.ctx, b.opID, pythonruntime.OperationOutcome{
			Succeeded:        succeeded,
			BytesTransferred: totalBytes,
			Error:            errMsg,
		})
	}
}

func buildRequestURL(rawURL string, params map[string]string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("url must include scheme and host: %q", rawURL)
	}
	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u, nil
}

func prepareRequestBody(req HTTPRequest) (io.Reader, int64, string, error) {
	if req.JSONBody != nil {
		raw, err := json.Marshal(req.JSONBody)
		if err != nil {
			return nil, 0, "", fmt.Errorf("marshal json body: %w", err)
		}
		return bytes.NewReader(raw), int64(len(raw)), "application/json", nil
	}
	if req.RawBody != nil {
		raw, err := io.ReadAll(req.RawBody)
		if err != nil {
			return nil, 0, "", fmt.Errorf("read request body: %w", err)
		}
		return bytes.NewReader(raw), int64(len(raw)), "", nil
	}
	return nil, 0, "", nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
