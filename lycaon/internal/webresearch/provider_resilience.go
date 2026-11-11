package webresearch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	providerHealthFailureThreshold = 2
	providerHealthCooldown         = 30 * time.Minute
)

// providerHealth pauses providers after consecutive hard failures.
type providerHealth struct {
	providerID string
	now        clockFunc

	mu                  sync.Mutex
	consecutiveFailures int
	coolUntil           time.Time
}

func newProviderHealth(providerID string, now clockFunc) *providerHealth {
	h := &providerHealth{providerID: providerID, now: now}
	if now == nil {
		h.now = time.Now
	}
	return h
}

func (h *providerHealth) isOpen() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.coolUntil.IsZero() && h.now().Before(h.coolUntil)
}

func (h *providerHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutiveFailures = 0
	h.coolUntil = time.Time{}
	h.mu.Unlock()
}

func (h *providerHealth) record(out providerOutcome) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if out.ok {
		h.consecutiveFailures = 0
		return
	}
	if !isHardProviderFailure(out) {
		return
	}
	h.consecutiveFailures++
	if h.consecutiveFailures >= providerHealthFailureThreshold {
		h.coolUntil = h.now().Add(providerHealthCooldown)
		h.consecutiveFailures = 0
	}
}

func isHardProviderFailure(out providerOutcome) bool {
	if out.ok {
		return false
	}
	switch out.reason {
	case "timeout", "parse_error", "http_error":
		// Pacing, authentication, and cancellation do not affect health.
		return true
	default:
		return false
	}
}

type healthWrappedProvider struct {
	inner  SearchProvider
	health *providerHealth
}

func (p *healthWrappedProvider) ID() string { return p.inner.ID() }

func (p *healthWrappedProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *healthWrappedProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *healthWrappedProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	if p.health != nil && p.health.isOpen() {
		return providerOutcome{
			providerID: p.inner.ID(),
			reason:     "down",
			detail:     "provider temporarily skipped after repeated failures",
		}
	}
	out := p.inner.Search(ctx, s, query, maxResults)
	if p.health != nil {
		p.health.record(out)
	}
	return out
}

// TransportFailureKind categorizes low-level network failures without query parameters.
type TransportFailureKind string

const (
	TransportFailureTimeout  TransportFailureKind = "timeout"
	TransportFailureDNS      TransportFailureKind = "dns"
	TransportFailureRefused  TransportFailureKind = "refused"
	TransportFailureTLS      TransportFailureKind = "tls"
	TransportFailureCanceled TransportFailureKind = "canceled"
	TransportFailureOther    TransportFailureKind = "transport"
)

// ProviderTransportError masks raw *url.Error text to prevent query parameter/secret leakage.
type ProviderTransportError struct {
	Kind   TransportFailureKind
	Target string
	Cause  error
}

func (e *ProviderTransportError) Error() string {
	if e == nil {
		return ""
	}
	if e.Target != "" {
		return fmt.Sprintf("%s connecting to %s", e.Kind, e.Target)
	}
	return string(e.Kind)
}

func (e *ProviderTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func sanitizeProviderHTTPError(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	var existing *ProviderTransportError
	if errors.As(err, &existing) {
		return existing
	}
	target := ""
	if req != nil && req.URL != nil {
		target = req.URL.Scheme + "://" + req.URL.Host
	}
	var urlErr *url.Error
	cause := err
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		cause = urlErr.Err
	}
	kind := classifyTransportFailure(cause, req)
	return &ProviderTransportError{
		Kind:   kind,
		Target: target,
		Cause:  cause,
	}
}

func classifyTransportFailure(err error, req *http.Request) TransportFailureKind {
	if req != nil && req.Context() != nil {
		if errors.Is(req.Context().Err(), context.Canceled) {
			return TransportFailureCanceled
		}
		if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
			return TransportFailureTimeout
		}
	}
	if errors.Is(err, context.Canceled) {
		return TransportFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return TransportFailureTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return TransportFailureTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return TransportFailureDNS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return TransportFailureRefused
	}
	var tlsRecordErr tls.RecordHeaderError
	if errors.As(err, &tlsRecordErr) {
		return TransportFailureTLS
	}
	var certErr x509.CertificateInvalidError
	if errors.As(err, &certErr) {
		return TransportFailureTLS
	}
	var authErr x509.UnknownAuthorityError
	if errors.As(err, &authErr) {
		return TransportFailureTLS
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return TransportFailureTLS
	}
	return TransportFailureOther
}

const defaultQueryBackoffMinWords = 3

// queryBackoffProvider wraps REST providers so long agent queries retry with
// trailing words dropped when an index returns no hits.
type queryBackoffProvider struct {
	inner    SearchProvider
	minWords int
}

func (p *queryBackoffProvider) ID() string { return p.inner.ID() }

func (p *queryBackoffProvider) Kind() ProviderKind { return p.inner.Kind() }

func (p *queryBackoffProvider) Configured(s Settings) bool { return p.inner.Configured(s) }

func (p *queryBackoffProvider) Search(ctx context.Context, s Settings, query string, maxResults int) providerOutcome {
	for i, candidate := range backoffQueryVariants(query, p.minWords) {
		candidateCtx := ctx
		// Retry variants may wait for this search's pacing reservation.
		if i > 0 && providerGateWaitFrom(ctx) <= 0 && s.PerProviderTimeoutSec > 0 {
			candidateCtx = withProviderGateWait(ctx, time.Duration(s.PerProviderTimeoutSec)*time.Second)
		}
		out := p.inner.Search(candidateCtx, s, candidate, maxResults)
		if !out.ok || len(out.hits) > 0 {
			return out
		}
	}
	return providerOutcome{providerID: p.ID(), ok: true, reason: "ok", hits: nil}
}

// backoffQueryVariants returns the query followed by progressively shorter
// prefixes down to minWords words.
func backoffQueryVariants(query string, minWords int) []string {
	words := strings.Fields(strings.TrimSpace(query))
	if len(words) == 0 {
		return nil
	}
	if minWords < 1 {
		minWords = 1
	}
	out := make([]string, 0, len(words))
	for n := len(words); n >= minWords; n-- {
		out = append(out, strings.Join(words[:n], " "))
	}
	if len(out) == 0 {
		out = append(out, strings.Join(words, " "))
	}
	return out
}

// ttlCache is the shared shape of the package's in-process crawl caches: TTL
// expiry on read, oldest-entry eviction at capacity. Not for stateful
// registries (polite gates, seen-URL scopes) — those mutate entries in place
// and keep their own eviction.
type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value V
	at    time.Time
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, entries: make(map[string]ttlEntry[V])}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[V]) put(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		oldestKey, oldestAt := "", time.Time{}
		for k, e := range c.entries {
			if oldestKey == "" || e.at.Before(oldestAt) {
				oldestKey, oldestAt = k, e.at
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = ttlEntry[V]{value: value, at: time.Now()}
}

func (c *ttlCache[V]) reset() {
	c.mu.Lock()
	c.entries = make(map[string]ttlEntry[V])
	c.mu.Unlock()
}
