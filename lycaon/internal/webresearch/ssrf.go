package webresearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/httpclient"
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
		accept = acceptHeaderForMode("text")
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
	for hop := 0; hop <= maxFetchRedirects; hop++ {
		var releaseHop func()
		if beforeHop != nil {
			var err error
			releaseHop, err = beforeHop(ctx, current)
			if err != nil {
				return nil, "", err
			}
		}
		host := current.Hostname()
		// Redirect targets pass through the same host gate.
		if err := egressgate.AwaitHost(ctx, host); err != nil {
			callIfSet(releaseHop)
			return nil, "", err
		}
		ioCtx := ctx
		var ioCancel context.CancelFunc
		if egressgate.From(ctx) != nil {
			// Approval time does not consume the network budget.
			ioCtx, ioCancel = egressgate.IOContext(ctx, defaultFetchTimeout)
		}
		ips, err := resolvePublicIPs(ioCtx, host)
		if err != nil {
			if ioCancel != nil {
				ioCancel()
			}
			callIfSet(releaseHop)
			return nil, "", err
		}
		transport := egress.PinnedTransport(host, ips, 0)
		client := httpclient.WithTransport(httpclient.Streaming(egressclass.WebResearchRequest), transport)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, err := http.NewRequestWithContext(ioCtx, http.MethodGet, current.String(), nil)
		if err != nil {
			if ioCancel != nil {
				ioCancel()
			}
			callIfSet(releaseHop)
			return nil, "", err
		}
		req.Header.Set("User-Agent", fetchUserAgent)
		req.Header.Set("Accept", accept)

		resp, err := client.Do(req)
		if err != nil {
			if ioCancel != nil {
				ioCancel()
			}
			callIfSet(releaseHop)
			return nil, "", err
		}
		if recordHop != nil {
			recordHop(current, resp)
		}
		if !isRedirectStatus(resp.StatusCode) {
			callbacks := make([]func(), 0, 2)
			if ioCancel != nil {
				callbacks = append(callbacks, ioCancel)
			}
			if releaseHop != nil {
				callbacks = append(callbacks, releaseHop)
			}
			if len(callbacks) > 0 {
				resp.Body = &callbacksOnCloseBody{ReadCloser: resp.Body, callbacks: callbacks}
			}
			return resp, current.String(), nil
		}
		loc := resp.Header.Get("Location")
		_ = resp.Body.Close()
		if ioCancel != nil {
			ioCancel()
		}
		callIfSet(releaseHop)
		transport.CloseIdleConnections()
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

// callbacksOnCloseBody releases per-hop resources with the response body.
type callbacksOnCloseBody struct {
	io.ReadCloser
	callbacks []func()
	once      sync.Once
}

func (b *callbacksOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		for _, callback := range b.callbacks {
			callback()
		}
	})
	return err
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
