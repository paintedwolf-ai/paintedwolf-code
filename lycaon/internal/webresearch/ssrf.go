package webresearch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// guardedGet validates and pins every redirect hop before dialing.
func guardedGet(ctx context.Context, u *url.URL, accept string) (*http.Response, string, error) {
	return guardedGetWithHopPolicy(ctx, u, accept, nil, nil)
}

type beforeGuardedGetHop func(context.Context, *url.URL) (func(), error)
type recordGuardedGetHop func(*url.URL, *http.Response)

func guardedGetWithHopPolicy(
	ctx context.Context,
	u *url.URL,
	accept string,
	beforeHop beforeGuardedGetHop,
	recordHop recordGuardedGetHop,
) (*http.Response, string, error) {
	if accept == "" {
		accept = researchAcceptHeader
	}
	current := u
	// Redirects add no new agent-authored content.
	if current != nil {
		screenedURL, err := screenOutbound(ctx, secretmatch.SurfaceFetchURL, httpDestination(current), current.String())
		if err != nil {
			return nil, "", err
		}
		if screenedURL != current.String() {
			current, err = normalizeFetchURL(screenedURL)
			if err != nil {
				return nil, "", err
			}
		}
	}
	hopHeaders := []outboundhttp.Header{
		{Name: "User-Agent", Value: fetchUserAgent},
		{Name: "Accept", Value: accept},
	}
	for hop := 0; hop <= maxFetchRedirects; hop++ {
		var releaseHop func()
		if beforeHop != nil {
			var err error
			releaseHop, err = beforeHop(ctx, current)
			if err != nil {
				return nil, "", err
			}
		}
		// Each hop is its own door exchange: the door validates, pins, and
		// gates the dial, and RedirectNone hands the redirect decision back
		// so every hop is revalidated here.
		resp, err := outboundhttp.Do(ctx, outboundhttp.Request{
			Class:        egressclass.WebResearchRequest,
			Method:       http.MethodGet,
			URL:          current.String(),
			HopHeaders:   hopHeaders,
			Timeout:      defaultFetchTimeout,
			MaxBodyBytes: fetchBodyByteLimit,
			AllowAddress: func(addr netip.Addr, _ uint16) bool { return ipAllowed(addr) },
			BeforeHop: func(hopCtx context.Context, hopURL *url.URL) error {
				// Redirect targets pass through the same host gate.
				return egressgate.AwaitHost(hopCtx, hopURL.Hostname())
			},
		})
		var hopResp *http.Response
		if err == nil {
			hopResp = guardedHopResponse(resp)
			if recordHop != nil {
				recordHop(current, hopResp)
			}
		}
		// The door buffers the body, so the hop's resources are free once it
		// returns; the polite slot no longer waits on the caller's read.
		callIfSet(releaseHop)
		if err != nil {
			return nil, "", err
		}
		if !isRedirectStatus(resp.Status) {
			return hopResp, current.String(), nil
		}
		loc := responseHeader(resp.Headers, "Location")
		if loc == "" {
			return nil, "", fmt.Errorf("redirect without a location header")
		}
		ref, err := url.Parse(loc)
		if err != nil {
			return nil, "", fmt.Errorf("invalid redirect location: %w", err)
		}
		next, err := normalizeFetchURL(current.ResolveReference(ref).String())
		if err != nil {
			return nil, "", err
		}
		current = next
	}
	return nil, "", fmt.Errorf("too many redirects")
}

func callIfSet(fn func()) {
	if fn != nil {
		fn()
	}
}

// guardedHopResponse adapts the door's buffered response to the *http.Response
// the fetch helpers read.
func guardedHopResponse(resp outboundhttp.Response) *http.Response {
	header := make(http.Header, len(resp.Headers))
	for _, h := range resp.Headers {
		header.Add(h.Name, h.Value)
	}
	return &http.Response{
		StatusCode:    resp.Status,
		Header:        header,
		ContentLength: int64(len(resp.Body)),
		Body:          io.NopCloser(bytes.NewReader(resp.Body)),
	}
}

// responseHeader returns the first value of the named response header.
func responseHeader(headers []outboundhttp.Header, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// maxFetchRedirects bounds the validated redirect loop.
const maxFetchRedirects = 10

// ipAllowed permits loopback only in package tests or the isolated development harness.
var ipAllowed = func(addr netip.Addr) bool {
	if addr.Unmap().IsLoopback() && configdir.IsHarnessChannel() {
		return true
	}
	return egress.IPPublic(addr)
}

// InvalidURLError reports an invalid fetch target.
type InvalidURLError struct {
	Detail string
	Cause  error
}

func (e *InvalidURLError) Error() string {
	if e == nil {
		return "invalid url"
	}
	return e.Detail
}

func (e *InvalidURLError) Unwrap() error { return e.Cause }

// normalizeFetchURL validates the target and strips credentials.
func normalizeFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, &InvalidURLError{Detail: fmt.Sprintf("invalid url: %v", err), Cause: err}
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, &InvalidURLError{Detail: "url must use http or https"}
	}
	if u.Hostname() == "" {
		return nil, &InvalidURLError{Detail: "url is missing a host"}
	}
	u.User = nil
	return u, nil
}

func resolvePublicIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	return resolveIPsWithPolicy(ctx, host, ipAllowed)
}

func resolveIPsWithPolicy(ctx context.Context, host string, allow func(netip.Addr) bool) ([]netip.Addr, error) {
	if allow == nil {
		allow = ipAllowed
	}
	return egress.ResolveIPsWithPolicy(ctx, host, allow)
}
