package hostproxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Proxy policy follows the pinned address. A loopback name pinned to a
// remote address is tunnelled through the configured proxy, not dialed
// directly past it.
func TestLoopbackNamePinnedElsewhereUsesTheProxy(t *testing.T) {
	directDesktop(t)
	remote := netip.MustParseAddrPort("[2001:db8::7]:443")
	connects := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects <- r.Method + " " + r.Host
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
	}))
	defer proxy.Close()
	endpoint, err := url.Parse(proxy.URL)
	testutil.FailErr(t, "parse proxy", err)
	t.Setenv("HTTPS_PROXY", endpoint.String())

	connection, err := DialPinned(t.Context(), mustURL(t, "https://localhost/x"), "tcp", remote)
	testutil.FailErr(t, "dial pinned remote", err)
	_ = connection.Close()
	if got := <-connects; got != "CONNECT "+net.JoinHostPort("2001:db8::7", "443") {
		t.Fatalf("proxy saw %q, want a CONNECT to the pinned address", got)
	}
}
