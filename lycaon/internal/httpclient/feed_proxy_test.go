package httpclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFeedUsesAuthenticatedProxyForEachPinnedHopAndPreservesTLSIdentity(t *testing.T) {
	var names, authorities, proxyTargets []string
	var mu sync.Mutex
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		names = append(names, r.TLS.ServerName)
		authorities = append(authorities, r.Host)
		mu.Unlock()
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy authentication reached origin")
		}
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "https://next.example.com:"+r.URL.Query().Get("port")+"/feed")
			w.WriteHeader(302)
		} else {
			_, _ = io.WriteString(w, "{}")
		}
	}))
	defer origin.Close()
	pinned := origin.Listener.Addr().String()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxyTargets = append(proxyTargets, r.Host)
		mu.Unlock()
		if r.Method != http.MethodConnect || r.Host != pinned {
			t.Error("proxy re-resolved or changed the authorized IP")
			w.WriteHeader(403)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic YWxpY2U6cHJpdmF0ZS1zZWNyZXQ=" {
			t.Error("proxy authentication missing")
			w.WriteHeader(407)
			return
		}
		upstream, err := net.Dial("tcp", pinned)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer func() { _ = upstream.Close() }()
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
		finished := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, connection); close(finished) }()
		_, _ = io.Copy(connection, upstream)
		_ = connection.Close()
		_ = upstream.Close()
		<-finished
	}))
	defer proxy.Close()
	endpoint, err := url.Parse(proxy.URL)
	testutil.FailErr(t, "parse authenticated proxy", err)
	endpoint.User = url.UserPassword("alice", "private-secret")
	t.Setenv("HTTPS_PROXY", endpoint.String())
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	originURL, err := url.Parse(origin.URL)
	testutil.FailErr(t, "parse pinned TLS origin", err)
	body, err := getFeed(t.Context(), "https://example.com:"+originURL.Port()+"/start?port="+originURL.Port(), feedTestOptions(), feedTLSNetwork(t, origin))
	testutil.FailErr(t, "fetch through authenticated pinned hops", err)
	mu.Lock()
	defer mu.Unlock()
	if string(body) != "{}" || len(names) != 2 || names[0] != "example.com" || names[1] != "next.example.com" || len(proxyTargets) != 2 || proxyTargets[0] != pinned || proxyTargets[1] != pinned {
		t.Fatalf("body=%q SNI=%v origins=%v proxy targets=%v", body, names, authorities, proxyTargets)
	}
}
