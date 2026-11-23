package webresearch

import (
	"context"
	"crypto/x509"
	"fmt"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/testutil"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

type providerHTTPFixture struct {
	*httptest.Server
	URL      string
	exchange Exchange
}

func withProviderHTTP(t *testing.T, handler http.HandlerFunc) *providerHTTPFixture {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse provider listener", err)
	listener, err := netip.ParseAddrPort(target.Host)
	testutil.FailErr(t, "parse provider address", err)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	fixture := &providerHTTPFixture{Server: server, URL: fmt.Sprintf("https://example.com:%d", listener.Port())}
	fixture.exchange = func(ctx context.Context, request outboundhttp.Request) (outboundhttp.Response, error) {
		allowed := request.AllowAddress
		request.AllowAddress = func(addr netip.Addr, port uint16) bool {
			if addr == listener.Addr() && port == listener.Port() {
				return true
			}
			return allowed != nil && allowed(addr, port)
		}
		request.Resolve = append(request.Resolve, outboundhttp.ResolveMapping{Host: "example.com", Port: listener.Port(), Address: listener.Addr(), TargetPort: listener.Port()})
		request.TLS = &outboundhttp.ClientTLS{Roots: roots}
		return outboundhttp.Do(ctx, request)
	}
	return fixture
}
