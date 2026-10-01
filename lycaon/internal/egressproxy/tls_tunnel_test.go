package egressproxy_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConnectPreservesOriginCertificateAndTLSVerification(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer origin.Close()
	p := brokerWith(func(context.Context, string, egressproxy.Endpoint) bool { return true }, leasedPeer("cmd"))
	p.SetLoopbackConnectAuthority(func(_ string, port uint16) bool { return port == serverPort(t, origin.URL) })
	_, err := p.Start()
	testutil.FailErr(t, "start proxy", err)
	defer func() { _ = p.Close() }()
	proxyURL, err := url.Parse("http://" + p.Addr())
	testutil.FailErr(t, "parse proxy URL", err)
	for _, trustOrigin := range []bool{true, false} {
		t.Run(strconv.FormatBool(trustOrigin), func(t *testing.T) {
			roots := x509.NewCertPool()
			if trustOrigin {
				roots.AddCert(origin.Certificate())
			}
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			response, err := client.Get(origin.URL)
			if !trustOrigin {
				if err == nil {
					_ = response.Body.Close()
					t.Fatal("tunnel bypassed origin certificate verification")
				}
				var untrusted x509.UnknownAuthorityError
				if !errors.As(err, &untrusted) {
					t.Fatalf("untrusted origin failed for another reason: %v", err)
				}
				return
			}
			testutil.FailErr(t, "request trusted origin through CONNECT", err)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || !bytes.Equal(response.TLS.PeerCertificates[0].Raw, origin.Certificate().Raw) {
				t.Fatal("CONNECT replaced the origin certificate or failed to verify the origin")
			}
		})
	}
}
