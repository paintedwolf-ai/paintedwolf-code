// Package outboundhttp provides the bounded first-party HTTP transport.
package outboundhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpclient"
)

const (
	DefaultTimeout       = 30 * time.Second
	MaxTimeout           = 5 * time.Minute
	DefaultBodyMax       = 5 << 20
	DefaultRedirectLimit = 5
	MaxRedirectLimit     = 10
)

type RedirectMode string

const (
	RedirectNone RedirectMode = "none"
	RedirectSafe RedirectMode = "safe"
)

// Header preserves caller order in receipts. net/http canonicalizes the wire.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HopTiming struct {
	DNS        int64 `json:"dns"`
	TCPConnect int64 `json:"tcp_connect"`
	TLS        int64 `json:"tls,omitempty"`
	TTFB       int64 `json:"ttfb"`
	Transfer   int64 `json:"transfer,omitempty"`
	Total      int64 `json:"total"`
}

type TLSInfo struct {
	Version         string               `json:"version"`
	CipherSuite     string               `json:"cipher_suite"`
	ALPN            string               `json:"alpn,omitempty"`
	PeerCertificate *PeerCertificateInfo `json:"peer_certificate,omitempty"`
}

type PeerCertificateInfo struct {
	Subject   string   `json:"subject"`
	Issuer    string   `json:"issuer"`
	ExpiresAt string   `json:"expires_at"`
	DNSNames  []string `json:"dns_names,omitempty"`
}

type Request struct {
	Class  egressclass.ID
	Method string
	URL    string
	// OriginHeaders are caller-authored and remain scoped to the declared origin.
	OriginHeaders []Header
	// HopHeaders are host-authored protocol headers sent on every approved hop.
	HopHeaders    []Header
	Body          []byte
	Timeout       time.Duration
	Redirects     RedirectMode
	RedirectLimit int
	MaxBodyBytes  int64
	DiscardBody   bool
	// StreamBody receives a size-limited reader; the receipt records its bytes and digest.
	StreamBody func(io.Reader) error
	// UnixSocket dials a local Unix domain socket instead of TCP when set.
	UnixSocket string
	// HostHeader overrides the Host header sent on wire.
	HostHeader string
	// Resolve maps hostnames to specific target IP and port.
	Resolve []ResolveMapping
	// Jar scopes stored cookies to each hop's URL.
	Jar          http.CookieJar
	AllowAddress func(netip.Addr, uint16) bool
	BeforeHop    func(context.Context, *url.URL) error
}

// Hop records a followed redirect and its destination.
type Hop struct {
	Status   int    `json:"status"`
	URL      string `json:"url"`
	Location string `json:"location"`
	// Method is present only when the redirect changes it.
	Method string     `json:"method,omitempty"`
	Timing *HopTiming `json:"timing_ms,omitempty"`
}

type Response struct {
	Status      int        `json:"status"`
	FinalURL    string     `json:"final_url"`
	Headers     []Header   `json:"headers,omitempty"`
	Redirects   []Hop      `json:"redirects,omitempty"`
	Body        []byte     `json:"-"`
	Bytes       int64      `json:"bytes"`
	SHA256      string     `json:"sha256"`
	ContentType string     `json:"content_type,omitempty"`
	DurationMS  int64      `json:"duration_ms"`
	Timing      *HopTiming `json:"timing_ms,omitempty"`
	TLS         *TLSInfo   `json:"tls,omitempty"`
	// BodyObserved excludes HEAD responses and discarded bodies.
	BodyObserved bool `json:"-"`
}

func NormalizeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, invalidRequest("invalid url: %s", err)
	}
	if u.User != nil {
		return nil, invalidRequest("url userinfo is not allowed")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, invalidRequest("url must use http or https")
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return nil, invalidRequest("url is missing a host")
	}
	// Fragments are client-side selectors and never cross the HTTP boundary.
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

func Do(ctx context.Context, in Request) (out Response, err error) {
	progress := ExchangeError{}
	defer func() {
		if err != nil {
			progress.Err = err
			err = &progress
		}
	}()
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	progress.Method = method
	prepared, err := prepareRequest(in, method)
	if err != nil {
		return Response{}, err
	}
	current := prepared.Target
	timeout := prepared.Timeout
	limit := prepared.MaxBodyBytes
	redirectMode := prepared.Redirects
	redirectLimit := prepared.RedirectLimit
	body := prepared.Body
	originHeaders := prepared.OriginHeaders
	hopHeaders := prepared.HopHeaders
	var hops []Hop
	var ioElapsed time.Duration
	for hop := 0; ; hop++ {
		if in.BeforeHop != nil {
			if err := in.BeforeHop(ctx, current); err != nil {
				return Response{}, err
			}
		}
		remaining := timeout - ioElapsed
		if remaining <= 0 {
			return Response{}, context.DeadlineExceeded
		}
		ioStarted := time.Now()
		// Approval waits use the session-stop context outside the turn deadline.
		ioCtx, ioCancel := context.WithTimeout(hitl.WaitContext(ctx), remaining)
		transport, dnsElapsed, err := in.transportForHop(ioCtx, current)
		if err != nil {
			ioCancel()
			return Response{}, err
		}
		client := in.clientForHop(remaining, transport)
		tracer, trace := newHopTracer()
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ioCtx, trace), method, current.String(), bytes.NewReader(body))
		if err != nil {
			transport.CloseIdleConnections()
			ioCancel()
			return Response{}, err
		}
		if in.HostHeader != "" {
			req.Host = in.HostHeader
		}
		for _, h := range hopHeaders {
			req.Header.Add(h.Name, h.Value)
		}
		for _, h := range originHeaders {
			req.Header.Add(h.Name, h.Value)
		}
		progress.RequestsStarted++
		resp, err := client.Do(req)
		if resp != nil {
			progress.ResponsesReceived++
			progress.LastStatus = resp.StatusCode
		}
		if err != nil {
			if classified := ClassifyTLSError(err); classified != nil {
				err = classified
			}
			transport.CloseIdleConnections()
			ioCancel()
			return Response{}, err
		}
		hopTiming := tracer.timing(ioStarted, dnsElapsed)
		if !isRedirect(resp.StatusCode) || redirectMode == RedirectNone {
			tlsInfo := extractTLSInfo(resp.TLS)
			result, readErr := readResponse(resp, method, current.String(), hops, limit, in.DiscardBody, in.StreamBody, hopTiming)
			ioElapsed += time.Since(ioStarted)
			hopTiming.Total = time.Since(ioStarted).Milliseconds()
			transport.CloseIdleConnections()
			ioCancel()
			result.DurationMS = ioElapsed.Milliseconds()
			result.Timing = hopTiming
			result.TLS = tlsInfo
			return result, readErr
		}
		_ = resp.Body.Close()
		ioElapsed += time.Since(ioStarted)
		hopTiming.Total = time.Since(ioStarted).Milliseconds()
		transport.CloseIdleConnections()
		ioCancel()
		adv, err := in.processRedirect(resp, current, method, body, originHeaders, hop, redirectLimit, hops, hopTiming)
		if err != nil {
			return Response{}, err
		}
		hops = append(hops, adv.record)
		method = adv.method
		current = adv.next
		body = adv.body
		originHeaders = adv.originHeaders
	}
}

type hopTracer struct {
	tcpStart, tcpDone time.Time
	tlsStart, tlsDone time.Time
	reqSent           time.Time
	ttfbTime          time.Time
}

func newHopTracer() (*hopTracer, *httptrace.ClientTrace) {
	t := &hopTracer{}
	trace := &httptrace.ClientTrace{
		ConnectStart: func(network, addr string) {
			if t.tcpStart.IsZero() {
				t.tcpStart = time.Now()
			}
		},
		ConnectDone: func(network, addr string, err error) {
			if t.tcpDone.IsZero() {
				t.tcpDone = time.Now()
			}
		},
		TLSHandshakeStart: func() {
			if t.tlsStart.IsZero() {
				t.tlsStart = time.Now()
			}
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if t.tlsDone.IsZero() {
				t.tlsDone = time.Now()
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if t.reqSent.IsZero() {
				t.reqSent = time.Now()
			}
		},
		GotFirstResponseByte: func() {
			if t.ttfbTime.IsZero() {
				t.ttfbTime = time.Now()
			}
		},
	}
	return t, trace
}

func (t *hopTracer) timing(ioStarted time.Time, dnsElapsed int64) *HopTiming {
	var tcpConnect, tlsDuration, ttfb int64
	if !t.tcpDone.IsZero() && !t.tcpStart.IsZero() {
		tcpConnect = t.tcpDone.Sub(t.tcpStart).Milliseconds()
	}
	if !t.tlsDone.IsZero() && !t.tlsStart.IsZero() {
		tlsDuration = t.tlsDone.Sub(t.tlsStart).Milliseconds()
	}
	if !t.ttfbTime.IsZero() {
		if !t.reqSent.IsZero() {
			ttfb = t.ttfbTime.Sub(t.reqSent).Milliseconds()
		} else {
			ttfb = t.ttfbTime.Sub(ioStarted).Milliseconds() - dnsElapsed - tcpConnect - tlsDuration
			if ttfb < 0 {
				ttfb = 0
			}
		}
	}
	return &HopTiming{
		DNS:        dnsElapsed,
		TCPConnect: tcpConnect,
		TLS:        tlsDuration,
		TTFB:       ttfb,
	}
}

type redirectAdvance struct {
	next          *url.URL
	method        string
	body          []byte
	originHeaders []Header
	record        Hop
}

func (in Request) processRedirect(
	resp *http.Response,
	current *url.URL,
	method string,
	body []byte,
	originHeaders []Header,
	hop, redirectLimit int,
	hops []Hop,
	timing *HopTiming,
) (redirectAdvance, error) {
	if hop >= redirectLimit {
		return redirectAdvance{}, &RedirectError{Reason: "too many redirects", Hops: hops}
	}
	location := resp.Header.Get("Location")
	status := resp.StatusCode
	if location == "" {
		return redirectAdvance{}, &RedirectError{Reason: "redirect without a location header", Hops: hops}
	}
	ref, err := url.Parse(location)
	if err != nil {
		return redirectAdvance{}, &RedirectError{Reason: "invalid redirect location: " + err.Error(), Hops: hops}
	}
	next, err := NormalizeURL(current.ResolveReference(ref).String())
	if err != nil {
		return redirectAdvance{}, err
	}
	nextMethod, keepBody := redirectMethod(status, method)
	record := Hop{Status: status, URL: current.String(), Location: next.String(), Timing: timing}
	if nextMethod != method {
		record.Method = nextMethod
	}
	newHops := append(slices.Clone(hops), record)
	if !sameOrigin(current, next) {
		// Every hop dials the same socket, so another origin would only be
		// a different name for the reviewed daemon.
		if in.UnixSocket != "" {
			return redirectAdvance{}, &RedirectError{
				Reason: fmt.Sprintf("%d redirect to %s leaves the origin served by the unix socket", status, next.Host),
				Hops:   newHops,
			}
		}
		if keepBody && len(body) > 0 {
			// The body was screened against the declared destination only.
			return redirectAdvance{}, &RedirectError{
				Reason: fmt.Sprintf("%d redirect to %s would re-send the request body to another origin", status, next.Host),
				Hops:   newHops,
			}
		}
		// Cross-origin redirects drop caller headers.
		originHeaders = nil
	}
	if !keepBody {
		body = nil
		originHeaders = withoutBodyHeaders(originHeaders)
	}
	return redirectAdvance{
		next:          next,
		method:        nextMethod,
		body:          body,
		originHeaders: originHeaders,
		record:        record,
	}, nil
}

func (in Request) transportForHop(ctx context.Context, target *url.URL) (*http.Transport, int64, error) {
	if in.UnixSocket != "" {
		return egress.UnixSocketTransport(in.UnixSocket), 0, nil
	}
	port, err := EffectivePort(target)
	if err != nil {
		return nil, 0, err
	}
	if addr, targetPort, mapped := MappedDial(in.Resolve, target.Hostname(), port); mapped {
		if !egress.IPPublic(addr) {
			if in.AllowAddress == nil || !in.AllowAddress(addr, targetPort) {
				return nil, 0, &egress.DestinationDeniedError{
					Reason: fmt.Sprintf("blocked: %s:%d is not an allowed address", addr, targetPort),
				}
			}
		}
		return egress.PinnedTransport(target.Hostname(), []netip.Addr{addr}, targetPort), 0, nil
	}
	dnsStart := time.Now()
	ips, err := egress.ResolveIPsWithPolicy(ctx, target.Hostname(), func(ip netip.Addr) bool {
		if egress.IPPublic(ip) {
			return true
		}
		return in.AllowAddress != nil && in.AllowAddress(ip, port)
	})
	dnsElapsed := time.Since(dnsStart).Milliseconds()
	if err != nil {
		return nil, dnsElapsed, err
	}
	return egress.PinnedTransport(target.Hostname(), ips, 0), dnsElapsed, nil
}

func (in Request) clientForHop(remaining time.Duration, transport *http.Transport) *http.Client {
	class := in.Class
	if class == "" {
		class = egressclass.WebResearchRequest
	}
	var client *http.Client
	if class == egressclass.AgentHTTPRequest {
		// Each hop uses the exchange's remaining time.
		client = httpclient.WithTransport(httpclient.Bounded(class, remaining), transport)
	} else {
		client = httpclient.WithTransport(httpclient.Streaming(class), transport)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = in.Jar
	return client
}

// withoutBodyHeaders drops the fields that describe a body a hop does not send.
func withoutBodyHeaders(headers []Header) []Header {
	out := headers[:0]
	for _, header := range headers {
		switch strings.ToLower(strings.TrimSpace(header.Name)) {
		case "content-type", "content-encoding", "content-language", "content-location":
			continue
		}
		out = append(out, header)
	}
	return out
}

func readResponse(resp *http.Response, method, finalURL string, hops []Hop, limit int64, discard bool, stream func(io.Reader) error, timing *HopTiming) (Response, error) {
	defer func() { _ = resp.Body.Close() }()
	readStart := time.Now()
	// A HEAD response carries no body, so there is nothing to size or digest.
	if discard || method == http.MethodHead {
		if timing != nil {
			timing.Transfer = time.Since(readStart).Milliseconds()
		}
		return Response{
			Status: resp.StatusCode, FinalURL: finalURL, Headers: responseHeaders(resp.Header),
			Redirects: hops, ContentType: resp.Header.Get("Content-Type"),
		}, nil
	}
	if resp.ContentLength > limit {
		return Response{}, &BodyTooLargeError{Limit: limit}
	}
	if stream != nil {
		res, err := streamResponse(resp, finalURL, hops, limit, stream)
		if timing != nil {
			timing.Transfer = time.Since(readStart).Milliseconds()
		}
		return res, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if timing != nil {
		timing.Transfer = time.Since(readStart).Milliseconds()
	}
	if err != nil {
		return Response{}, &TransferError{Err: err}
	}
	if int64(len(body)) > limit {
		return Response{}, &BodyTooLargeError{Limit: limit}
	}
	sum := sha256.Sum256(body)
	return Response{
		Status: resp.StatusCode, FinalURL: finalURL, Headers: responseHeaders(resp.Header),
		Redirects: hops, Body: body, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
		ContentType: resp.Header.Get("Content-Type"), BodyObserved: true,
	}, nil
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", version)
	}
}

func extractTLSInfo(state *tls.ConnectionState) *TLSInfo {
	if state == nil {
		return nil
	}
	info := &TLSInfo{
		Version:     tlsVersionName(state.Version),
		CipherSuite: tls.CipherSuiteName(state.CipherSuite),
		ALPN:        state.NegotiatedProtocol,
	}
	if len(state.PeerCertificates) > 0 {
		cert := state.PeerCertificates[0]
		info.PeerCertificate = &PeerCertificateInfo{
			Subject:   cert.Subject.String(),
			Issuer:    cert.Issuer.String(),
			ExpiresAt: cert.NotAfter.Format(time.RFC3339),
			DNSNames:  cert.DNSNames,
		}
	}
	return info
}

// streamResponse records bytes and a digest while enforcing the size limit.
func streamResponse(resp *http.Response, finalURL string, hops []Hop, limit int64, stream func(io.Reader) error) (Response, error) {
	hasher := sha256.New()
	bounded := &boundedReader{r: io.TeeReader(resp.Body, hasher), limit: limit}
	err := stream(bounded)
	switch {
	case bounded.exceeded:
		return Response{}, &BodyTooLargeError{Limit: limit}
	case bounded.transferErr != nil:
		// Transport read errors take precedence over sink errors.
		return Response{}, &TransferError{Err: bounded.transferErr}
	case err != nil:
		return Response{}, err
	}
	return Response{
		Status: resp.StatusCode, FinalURL: finalURL, Headers: responseHeaders(resp.Header),
		Redirects: hops, Bytes: bounded.n, SHA256: hex.EncodeToString(hasher.Sum(nil)),
		ContentType: resp.Header.Get("Content-Type"), BodyObserved: true,
	}, nil
}

// boundedReader records transfer errors and rejects reads that exceed the size limit.
type boundedReader struct {
	r           io.Reader
	limit       int64
	n           int64
	exceeded    bool
	transferErr error
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n > b.limit {
		b.exceeded = true
		return 0, &BodyTooLargeError{Limit: b.limit}
	}
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.limit {
		b.exceeded = true
		return n, &BodyTooLargeError{Limit: b.limit}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		b.transferErr = err
	}
	return n, err
}

// EffectivePort is the port a URL dials: its explicit port or its scheme default.
func EffectivePort(u *url.URL) (uint16, error) {
	if raw := u.Port(); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || n == 0 {
			return 0, invalidRequest("invalid url port")
		}
		return uint16(n), nil
	}
	return DefaultPort(u.Scheme), nil
}

// MappedDial returns the address and port a hop to host:port dials when a
// resolve mapping applies; the first mapping for the host and port wins.
func MappedDial(mappings []ResolveMapping, host string, port uint16) (netip.Addr, uint16, bool) {
	for _, m := range mappings {
		if strings.EqualFold(m.Host, host) && (m.Port == 0 || m.Port == port) {
			if m.TargetPort > 0 {
				return m.Address, m.TargetPort, true
			}
			return m.Address, port, true
		}
	}
	return netip.Addr{}, 0, false
}

// DefaultPort is the port an http or https URL without one dials.
func DefaultPort(scheme string) uint16 {
	if strings.EqualFold(scheme, "https") {
		return 443
	}
	return 80
}

func methodAllowed(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func validateHeader(h Header) error {
	name := h.Name
	if name != strings.TrimSpace(name) || !validHeaderName(name) || !validHeaderValue(h.Value) {
		return invalidRequest("invalid request header")
	}
	switch strings.ToLower(name) {
	case "host", "connection", "proxy-authorization", "proxy-connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade", "content-length":
		return invalidRequest("request header %q is controlled by the host", name)
	}
	return nil
}

// ValidateHeaders rejects malformed and host-controlled fields.
func ValidateHeaders(headers []Header) error {
	for _, header := range headers {
		if err := validateHeader(header); err != nil {
			return err
		}
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\t' || value[i] >= 0x20 && value[i] != 0x7f {
			continue
		}
		return false
	}
	return true
}

func responseHeaders(in http.Header) []Header {
	out := make([]Header, 0, len(in))
	for name, values := range in {
		switch strings.ToLower(name) {
		case "set-cookie", "proxy-authenticate":
			continue
		}
		for _, value := range values {
			out = append(out, Header{Name: name, Value: value})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Value < out[j].Value
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func isRedirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
