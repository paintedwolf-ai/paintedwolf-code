package mcp

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

// HTTPAuthHeaders builds static headers for an HTTP MCP provider.
// OAuth access tokens are applied separately when Authorization is unset.
func HTTPAuthHeaders(entry MCPProviderEntry) http.Header {
	h := make(http.Header)
	for k, v := range entry.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		h.Set(k, v)
	}
	name, value := formatStaticCredential(entry.CredentialWire, entry.CredentialHeader, entry.Token)
	if name != "" && h.Get(name) == "" {
		h.Set(name, value)
	}
	return h
}

type headerRoundTripper struct {
	base     http.RoundTripper
	headers  http.Header
	bearer   string // OAuth access token when Authorization is unset
	authHost string
	onStatus func(code int)
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	// Inject here and gate on authHost: net/http strips Authorization on
	// cross-host redirect, so this is the only host check that runs.
	if t.authorizedHost(req.URL) {
		for k, vals := range t.headers {
			for _, v := range vals {
				clone.Header.Set(k, v)
			}
		}
		if t.bearer != "" && clone.Header.Get("Authorization") == "" {
			clone.Header.Set("Authorization", "Bearer "+t.bearer)
		}
	}
	resp, err := base.RoundTrip(clone)
	if resp != nil && t.onStatus != nil {
		t.onStatus(resp.StatusCode)
	}
	return resp, err
}

// authorizedHost is true when u is the host these credentials belong to.
func (t *headerRoundTripper) authorizedHost(u *url.URL) bool {
	return u != nil && t.authHost != "" && strings.EqualFold(u.Hostname(), t.authHost)
}

func httpClientWithAuth(base *http.Client, headers http.Header, oauthBearer, authHost string, onStatus func(int)) *http.Client {
	if base == nil {
		base = httpclient.Bounded(egressclass.MCPRemoteHTTP, defaultHTTPClientTimeout)
	}
	if len(headers) == 0 && oauthBearer == "" && onStatus == nil {
		return base
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	out := *base
	out.Transport = &headerRoundTripper{
		base:     transport,
		headers:  headers.Clone(),
		bearer:   oauthBearer,
		authHost: strings.ToLower(strings.TrimSpace(authHost)),
		onStatus: onStatus,
	}
	return &out
}
