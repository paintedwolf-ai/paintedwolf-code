package webresearch

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	providerSeedCacheTTL = 3 * time.Minute
	providerSeedCacheMax = 256
)

// providerSeedCache reuses recent provider Search outcomes for identical
// seed probes. Concurrent coordinator searches often re-ask near-duplicate
// queries; caching cuts paced contention and duplicate RTT without changing
// who initiates the search.
var providerSeedCache = newTTLCache[providerOutcome](providerSeedCacheTTL, providerSeedCacheMax)

func providerSeedCacheKey(providerID, query string, maxResults int, settings Settings) string {
	return providerID + "\x00" + normalizeQueryKey(query) + "\x00" + strconv.Itoa(maxResults) +
		"\x00" + searchSettingsFingerprint(settings, providerID)
}

func cachedProviderSeed(providerID, query string, maxResults int, settings Settings) (providerOutcome, bool) {
	out, ok := providerSeedCache.get(providerSeedCacheKey(providerID, query, maxResults, settings))
	if !ok {
		return providerOutcome{}, false
	}
	if len(out.hits) > 0 {
		out.hits = append([]WebHit(nil), out.hits...)
	}
	return out, true
}

func storeProviderSeed(providerID, query string, maxResults int, settings Settings, out providerOutcome) {
	if !out.ok {
		return
	}
	// Clone hits so callers cannot mutate the cached slice.
	cached := out
	if len(out.hits) > 0 {
		cached.hits = append([]WebHit(nil), out.hits...)
	}
	providerSeedCache.put(providerSeedCacheKey(providerID, query, maxResults, settings), cached)
}

const providerSearchUserAgent = "painted-wolf-code-search/1.0"

// providerHTTPClient overrides the egress-pinned client in tests.
var providerHTTPClient *http.Client

// SetProviderHTTPClientForTest sets the REST client for boundary tests.
func SetProviderHTTPClientForTest(c *http.Client) {
	if os.Getenv("LYCAON_TEST") != "1" {
		panic("webresearch.SetProviderHTTPClientForTest requires LYCAON_TEST=1")
	}
	providerHTTPClient = c
}

func providerHTTPTimeout(timeoutSec int) time.Duration {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return timeout
}

// providerBodyMax bounds the provider response body, matching the previous
// LimitReader budget.
const providerBodyMax = 1 << 20

// newProviderRequest builds the provider request as inert data. The exchange
// itself goes through outboundhttp (or, under LYCAON_TEST, the injected seam
// client), so the request object never reaches the network directly.
func newProviderRequest(ctx context.Context, method, rawURL string, body []byte) (*http.Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	req := &http.Request{
		Method: method,
		URL:    u,
		Header: make(http.Header),
	}
	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return req.WithContext(ctx), nil
}

func doProviderHTTP(ctx context.Context, req *http.Request, timeoutSec int, allowPrivate bool) ([]byte, int, error) {
	// Apply the host gate before starting the network deadline.
	if err := egressgate.AwaitHost(ctx, req.URL.Hostname()); err != nil {
		return nil, 0, err
	}
	timeout := providerHTTPTimeout(timeoutSec)
	if providerHTTPClient != nil {
		// Validation precedes the injected transport; the seam keeps the
		// pre-door exchange shape for boundary tests.
		ioCtx, cancel := egressgate.IOContext(ctx, timeout)
		defer cancel()
		if _, err := providerEgressClient(ioCtx, req.URL, allowPrivate); err != nil {
			return nil, 0, err
		}
		return readProviderHTTP(providerHTTPClient, req.WithContext(ioCtx))
	}
	if _, err := validateProviderEndpoint(req.URL.String(), allowPrivate); err != nil {
		return nil, 0, err
	}
	return providerHTTPThroughDoor(ctx, req, timeout, allowPrivate)
}

// providerHTTPThroughDoor exchanges the built request through outboundhttp.
func providerHTTPThroughDoor(ctx context.Context, req *http.Request, timeout time.Duration, allowPrivate bool) ([]byte, int, error) {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, 0, err
		}
		body = b
	}
	doorReq := outboundhttp.Request{
		Class:  egressclass.WebResearchRequest,
		Method: req.Method,
		URL:    req.URL.String(),
		// Origin headers stay scoped to the declared provider origin; redirect
		// hops re-send only the host-authored headers.
		OriginHeaders: providerOriginHeaders(req.Header),
		Body:          body,
		Timeout:       timeout,
		Redirects:     outboundhttp.RedirectSafe,
		RedirectLimit: outboundhttp.MaxRedirectLimit,
		MaxBodyBytes:  providerBodyMax,
		AllowAddress: func(addr netip.Addr, _ uint16) bool {
			return ipProviderEgress(addr, allowPrivate)
		},
		BeforeHop: func(hopCtx context.Context, hopURL *url.URL) error {
			return egressgate.AwaitHost(hopCtx, hopURL.Hostname())
		},
	}
	started := time.Now()
	resp, err := outboundhttp.Do(ctx, doorReq)
	if err != nil {
		// A malformed automatic gzip decode is retried once under the same
		// deadline with an explicit identity encoding.
		if req.Method == http.MethodGet && len(body) == 0 && errors.Is(err, gzip.ErrHeader) {
			if remaining := timeout - time.Since(started); remaining > 0 {
				retry := doorReq
				retry.OriginHeaders = append(append([]outboundhttp.Header(nil), doorReq.OriginHeaders...),
					outboundhttp.Header{Name: "Accept-Encoding", Value: "identity"})
				retry.Timeout = remaining
				retryResp, retryErr := outboundhttp.Do(ctx, retry)
				if retryErr == nil {
					return retryResp.Body, retryResp.Status, nil
				}
				err = retryErr
			}
		}
		return nil, 0, sanitizeProviderHTTPError(req, err)
	}
	return resp.Body, resp.Status, nil
}

// providerOriginHeaders carries the built request headers to the door.
func providerOriginHeaders(header http.Header) []outboundhttp.Header {
	out := make([]outboundhttp.Header, 0, len(header))
	for name, values := range header {
		for _, value := range values {
			out = append(out, outboundhttp.Header{Name: name, Value: value})
		}
	}
	return out
}

func readProviderHTTP(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, sanitizeProviderHTTPError(req, err)
	}
	body, status, err := readProviderHTTPBody(resp)
	_ = resp.Body.Close()
	if !resp.Uncompressed || !errors.Is(err, gzip.ErrHeader) ||
		req.Method != http.MethodGet || (req.Body != nil && req.Body != http.NoBody) ||
		req.Context().Err() != nil {
		return body, status, err
	}

	// Retry malformed automatic gzip decoding once under the same deadline.
	retry := req.Clone(req.Context())
	retry.Header.Set("Accept-Encoding", "identity")
	resp, err = client.Do(retry)
	if err != nil {
		return nil, 0, sanitizeProviderHTTPError(retry, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readProviderHTTPBody(resp)
}

func readProviderHTTPBody(resp *http.Response) ([]byte, int, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

// providerEgressTestRelax permits loopback HTTP in provider tests.
var providerEgressTestRelax bool

// validateProviderEndpoint normalizes the URL and checks its address policy.
func validateProviderEndpoint(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := normalizeFetchURL(raw)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	if !allowPrivate && !providerEgressTestRelax && scheme != "https" {
		return nil, fmt.Errorf("provider endpoint must use https")
	}
	host := u.Hostname()
	if lit, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && !providerEgressTestRelax {
			return nil, fmt.Errorf("provider endpoint must use a hostname, not a bare IP")
		}
		if !ipProviderEgress(lit, allowPrivate) {
			return nil, fmt.Errorf("blocked: %s is not an allowed provider address", host)
		}
	}
	return u, nil
}

// ValidateProviderConfigEndpoint checks an endpoint before persistence.
func ValidateProviderConfigEndpoint(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := validateProviderEndpoint(raw, allowPrivate)
	if err != nil {
		return err
	}
	_, err = resolveProviderEgressIPs(ctx, u.Hostname(), allowPrivate)
	return err
}

// ipProviderEgress admits private ranges without admitting local infrastructure.
func ipProviderEgress(addr netip.Addr, allowPrivate bool) bool {
	if providerEgressTestRelax {
		return ipAllowed(addr)
	}
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if addr.IsLoopback() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	if allowPrivate && addr.IsPrivate() {
		return true
	}
	return egress.IPPublic(addr)
}

// resolveProviderEgressIPs resolves host under the provider egress policy.
func resolveProviderEgressIPs(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	return resolveIPsWithPolicy(ctx, host, func(a netip.Addr) bool {
		return ipProviderEgress(a, allowPrivate)
	})
}

// providerEgressClient pins validated addresses and applies the host gate.
func providerEgressClient(ctx context.Context, u *url.URL, allowPrivate bool) (*http.Client, error) {
	if _, err := validateProviderEndpoint(u.String(), allowPrivate); err != nil {
		return nil, err
	}
	host := u.Hostname()
	ips, err := resolveProviderEgressIPs(ctx, host, allowPrivate)
	if err != nil {
		return nil, err
	}
	if err := egressgate.AwaitHost(ctx, host); err != nil {
		return nil, err
	}
	// The request context sets the deadline.
	return httpclient.WithTransport(
		httpclient.Streaming(egressclass.WebResearchRequest),
		egress.PinnedTransport(u, ips, 0),
	), nil
}

// DeclaredEndpointHosts is the endpoint host set one web_search fan-out reaches
// (results providers plus probed seed providers), derived from catalog and stored
// config rather than the model. The approval gate coalesces on it; it grants
// nothing, so a dial outside the set keeps its own per-host ask.
func DeclaredEndpointHosts(settings Settings, reg *Registry) []string {
	if reg == nil {
		return nil
	}
	resolved := resolvedSearchProviders(settings, reg)
	hosts := make([]string, 0, len(resolved))
	for _, provider := range resolved {
		if host := endpointHost(provider.endpoint); host != "" {
			hosts = append(hosts, host)
		}
	}
	return confine.NormalizeDeclaredHosts(hosts)
}

// resolvedProvider is one configured provider a fan-out would reach, paired with
// its resolved endpoint. The endpoint is empty when it is not operator-settable.
type resolvedProvider struct {
	id       string
	endpoint string
}

// resolvedSearchProviders walks the providers one web_search fan-out reaches:
// every results provider plus every catalog seeds-role provider the direct
// pipeline will probe.
func resolvedSearchProviders(settings Settings, reg *Registry) []resolvedProvider {
	if reg == nil {
		return nil
	}
	ids := make([]string, 0, len(settings.EnabledProviders)+len(settings.SoftProviderIDs))
	ids = append(ids, settings.EnabledProviders...)
	ids = append(ids, settings.SoftProviderIDs...)
	ids = append(ids, resolveSeedProviders(settings, reg)...)

	out := make([]resolvedProvider, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || isDirectProvider(id) {
			continue
		}
		if !reg.Has(id) {
			continue
		}
		if p := reg.Get(id); p == nil || !p.Configured(settings) {
			continue
		}
		out = append(out, resolvedProvider{id: id, endpoint: keylessEndpoint(settings, id)})
	}
	return out
}

// endpointHost extracts the dialed host from a provider endpoint. Endpoints are
// validated elsewhere; an unparseable one contributes nothing rather than a
// half-parsed string that could never match a real dial anyway.
func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
