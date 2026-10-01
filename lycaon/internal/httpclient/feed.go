package httpclient

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
)

const feedRedirectLimit = 5

// FeedOptions binds an unauthenticated JSON feed to its purpose and bounds.
type FeedOptions struct {
	Class     egressclass.ID
	Timeout   time.Duration
	MaxBytes  bytebound.Transport
	UserAgent string
}

// GetFeed fetches a complete HTTPS document from public, pinned destinations.
// Timeout includes DNS, all redirects, and the decoded response body.
func GetFeed(ctx context.Context, rawURL string, opts FeedOptions) ([]byte, error) {
	return getFeed(ctx, rawURL, opts, feedNetwork{
		resolve: egress.ResolvePublicIPs,
		transport: func(host string, ips []netip.Addr) feedTransport {
			return egress.PinnedTransport(host, ips, 0)
		},
	})
}

type feedTransport interface {
	http.RoundTripper
	CloseIdleConnections()
}

type feedNetwork struct {
	resolve   func(context.Context, string) ([]netip.Addr, error)
	transport func(string, []netip.Addr) feedTransport
}

func getFeed(ctx context.Context, rawURL string, opts FeedOptions, network feedNetwork) ([]byte, error) {
	egressclass.RequireTransport(opts.Class, egressclass.HTTPBounded)
	if opts.Timeout <= 0 || opts.MaxBytes <= 0 || opts.MaxBytes == math.MaxInt64 {
		return nil, fmt.Errorf("feed timeout and byte limit must be positive and bounded")
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	current, err := feedURL(rawURL)
	if err != nil {
		return nil, err
	}
	for hop := 0; ; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := network.getHop(ctx, current, opts)
		if err != nil {
			return nil, err
		}
		if result.next == nil {
			return result.body, nil
		}
		if hop >= feedRedirectLimit {
			return nil, fmt.Errorf("feed exceeded %d redirects", feedRedirectLimit)
		}
		current = result.next
	}
}

type feedHop struct {
	body []byte
	next *url.URL
}

func (n feedNetwork) getHop(ctx context.Context, target *url.URL, opts FeedOptions) (feedHop, error) {
	ips, err := n.resolve(ctx, target.Hostname())
	if err != nil {
		return feedHop{}, err
	}
	transport := n.transport(target.Hostname(), ips)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       opts.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return feedHop{}, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return feedHop{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if feedRedirect(resp.StatusCode) {
		next, err := feedRedirectURL(target, resp.Header.Get("Location"))
		return feedHop{next: next}, err
	}
	body, err := readFeedResponse(resp, opts.MaxBytes)
	return feedHop{body: body}, err
}

func feedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid feed url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("feed url must use https")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("feed url is missing a host")
	}
	if port := u.Port(); port != "" {
		if value, err := strconv.ParseUint(port, 10, 16); err != nil || value == 0 {
			return nil, fmt.Errorf("feed url has an invalid port")
		}
	}
	u.Scheme = "https"
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

func feedRedirectURL(current *url.URL, location string) (*url.URL, error) {
	if location == "" {
		return nil, fmt.Errorf("feed redirect without location")
	}
	ref, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("invalid feed redirect: %w", err)
	}
	return feedURL(current.ResolveReference(ref).String())
}

func feedRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}
