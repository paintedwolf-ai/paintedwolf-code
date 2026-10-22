package hostproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/systemproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func directDesktop(t *testing.T) {
	t.Helper()
	previous := systemConfiguration
	systemConfiguration = func(context.Context) (systemproxy.Configuration, error) {
		return systemproxy.Configuration{Mode: systemproxy.ModeNone}, nil
	}
	t.Cleanup(func() { systemConfiguration = previous })
	clearProxyEnvironment(t)
}

func TestConnectPinsNumericDestinationAndContainsCredentialsInProxyHeader(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		t.Run(map[bool]string{false: "environment", true: "desktop"}[desktop], func(t *testing.T) {
			directDesktop(t)
			address := netip.MustParseAddrPort("[2001:db8::123]:443")
			observed := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- r.Method + " " + r.RequestURI + " " + r.Header.Get("Proxy-Authorization")
				if r.Host != address.String() || r.Header.Get("Authorization") != "" || strings.Contains(r.RequestURI, "secret") {
					t.Error("CONNECT changed the pinned authority or forwarded origin credentials")
				}
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = connection.Close() }()
				_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\nprebuffered")
			}))
			defer server.Close()
			endpoint, err := url.Parse(server.URL)
			testutil.FailErr(t, "parse proxy fixture", err)
			endpoint.User = url.UserPassword("alice", "proxy-secret")
			if desktop {
				useSystemProxy(t, endpoint.String())
			} else {
				t.Setenv("HTTPS_PROXY", endpoint.String())
			}
			target := mustURL(t, "https://origin-user:origin-secret@approval.invalid/private?token=origin-secret")
			connection, err := DialPinned(t.Context(), target, "tcp", address)
			testutil.FailErr(t, "connect through pinned proxy", err)
			defer func() { _ = connection.Close() }()
			raw, err := io.ReadAll(connection)
			testutil.FailErr(t, "read buffered tunnel bytes", err)
			if string(raw) != "prebuffered" {
				t.Fatalf("tunnel bytes = %q", raw)
			}
			if got := <-observed; got != "CONNECT "+address.String()+" Basic YWxpY2U6cHJveHktc2VjcmV0" {
				t.Fatalf("CONNECT envelope = %q", got)
			}
		})
	}
}

func TestProxyRefusalMalformedReplyAndCancellationNeverDialDirect(t *testing.T) {
	for _, response := range []string{
		"HTTP/1.1 407 Proxy Authentication Required\r\n\r\n",
		"HTTP/1.1 302 Found\r\nLocation: http://other.invalid/\r\n\r\n",
		"not-http\r\n\r\n", "HTTP/1.1 200 OK\r\nX-Large: " + strings.Repeat("s", 17000) + "\r\n\r\n",
	} {
		directDesktop(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = connection.Close() }()
			_, _ = io.WriteString(connection, response)
		}))
		endpoint, err := url.Parse(server.URL)
		testutil.FailErr(t, "parse refusal fixture", err)
		endpoint.User = url.UserPassword("alice", "private-secret")
		t.Setenv("HTTPS_PROXY", endpoint.String())
		connection, err := DialPinned(t.Context(), mustURL(t, "https://never-resolve.invalid/"), "tcp", netip.MustParseAddrPort("192.0.2.1:443"))
		if connection != nil || err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("proxy refusal = %v, %v", connection, err)
		}
		server.Close()
	}
	directDesktop(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen for cancellation", err)
	defer func() { _ = listener.Close() }()
	closed := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close(); close(closed) }()
		_, _ = io.Copy(io.Discard, connection)
	}()
	t.Setenv("HTTPS_PROXY", "http://"+listener.Addr().String())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err = DialPinned(ctx, mustURL(t, "https://approval.invalid/"), "tcp", netip.MustParseAddrPort("192.0.2.1:443"))
	if err == nil {
		t.Fatal("overdue proxy became a direct connection")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled proxy connection stayed open")
	}
}

func TestNoProxyUsesOriginalDestinationAndExplicitPolicyDoesNotReadDesktop(t *testing.T) {
	directDesktop(t)
	systemConfiguration = func(context.Context) (systemproxy.Configuration, error) {
		t.Error("explicit policy read desktop state")
		return systemproxy.Configuration{}, errors.New("unavailable")
	}
	t.Setenv("HTTPS_PROXY", "invalid://proxy-secret")
	t.Setenv("NO_PROXY", "approval.invalid")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen direct fixture", err)
	defer func() { _ = listener.Close() }()
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			_, _ = io.WriteString(connection, "direct")
			_ = connection.Close()
		}
	}()
	address := netip.MustParseAddrPort(listener.Addr().String())
	connection, err := DialPinned(t.Context(), mustURL(t, "https://approval.invalid/"), "tcp", address)
	testutil.FailErr(t, "dial explicit exception", err)
	defer func() { _ = connection.Close() }()
	line, err := bufio.NewReader(connection).ReadString('\n')
	if line != "direct" || err != io.EOF {
		t.Fatalf("direct reply = %q, %v", line, err)
	}
	if _, err := Resolve(mustURL(t, "https://other.invalid/")); err == nil {
		t.Fatal("invalid explicit proxy was ignored")
	}
}

func TestHTTPSProxyRequiresItsOwnValidCertificate(t *testing.T) {
	directDesktop(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted HTTPS proxy reached CONNECT") }))
	defer server.Close()
	t.Setenv("HTTPS_PROXY", server.URL)
	_, err := DialPinned(t.Context(), mustURL(t, "https://approval.invalid/"), "tcp", netip.MustParseAddrPort("192.0.2.1:443"))
	if err != ProxyTLS {
		t.Fatalf("HTTPS proxy trust failure = %v", err)
	}
}
