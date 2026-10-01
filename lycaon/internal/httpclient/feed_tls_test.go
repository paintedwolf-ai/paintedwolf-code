package httpclient

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
)

func feedTLSNetwork(t *testing.T, srv *httptest.Server) feedNetwork {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return feedNetwork{
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		transport: func(host string, ips []netip.Addr) feedTransport {
			tr := egress.PinnedTransport(host, ips, 0)
			if tr.Proxy != nil {
				t.Fatal("pinned transport enables a proxy")
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			return tr
		},
	}
}

func TestFeedTLSRetainsHostnameAndDisablesProxies(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var hosts, serverNames []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		serverNames = append(serverNames, r.TLS.ServerName)
		mu.Unlock()
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "https://next.example.com:"+r.URL.Query().Get("port")+"/feed")
			w.WriteHeader(302)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	testutil.FailErr(t, "parse TLS server", err)
	for _, class := range []egressclass.ID{egressclass.PricingFeedRefresh, egressclass.ModelMetadataRefresh} {
		opts := feedTestOptions()
		opts.Class = class
		mu.Lock()
		hosts = nil
		serverNames = nil
		mu.Unlock()
		body, err := getFeed(t.Context(), "https://example.com:"+u.Port()+"/start?port="+u.Port(), opts, feedTLSNetwork(t, srv))
		testutil.FailErr(t, "fetch pinned TLS feed", err)
		mu.Lock()
		gotHosts, gotNames := append([]string(nil), hosts...), append([]string(nil), serverNames...)
		mu.Unlock()
		if string(body) != "{}" || !reflect.DeepEqual(gotHosts, []string{"example.com:" + u.Port(), "next.example.com:" + u.Port()}) || !reflect.DeepEqual(gotNames, []string{"example.com", "next.example.com"}) {
			t.Fatalf("body=%q hosts=%v SNI=%v", body, gotHosts, gotNames)
		}
	}
	_, err = getFeed(t.Context(), "https://wrong.invalid:"+u.Port()+"/feed", feedTestOptions(), feedTLSNetwork(t, srv))
	var hostnameError x509.HostnameError
	if !errors.As(err, &hostnameError) {
		t.Fatalf("TLS hostname mismatch was not rejected: %v", err)
	}
}

func TestFeedRejectsCompressedAndChunkedOverflow(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "chunked", true: "gzip"}[compressed], func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				body := "{}" + strings.Repeat(" ", 1024)
				if compressed {
					z := gzip.NewWriter(w)
					_, _ = io.WriteString(z, body)
					_ = z.Close()
				} else {
					_, _ = io.WriteString(w, body)
				}
			}))
			defer srv.Close()
			u, err := url.Parse(srv.URL)
			testutil.FailErr(t, "parse TLS server", err)
			opts := feedTestOptions()
			opts.MaxBytes = 128
			body, err := getFeed(t.Context(), "https://example.com:"+u.Port()+"/feed", opts, feedTLSNetwork(t, srv))
			var oversized *ResponseBodyTooLargeError
			if !errors.As(err, &oversized) || body != nil {
				t.Fatalf("body=%q err=%v", body, err)
			}
		})
	}
}
