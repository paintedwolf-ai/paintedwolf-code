package outboundhttp

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/tlstest"
)

func TestClientCertificateOpensPEMIdentities(t *testing.T) {
	ca := tlstest.NewCA(t, "Client CA")
	leaf := ca.Client(t, "deploy-bot")
	other := ca.Client(t, "someone-else")
	encrypted := tlstest.EncryptPKCS8(t, leaf.KeyDER, "correct horse")
	block, _ := pem.Decode(leaf.KeyPEM)
	legacy := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: block.Bytes})
	corrupt, _ := pem.Decode(encrypted)
	corrupt.Bytes = append([]byte(nil), corrupt.Bytes...)
	corrupt.Bytes[len(corrupt.Bytes)-1] ^= 0xff
	for _, c := range []struct {
		name          string
		cert, key     []byte
		pass, failure string
	}{
		{name: "separate key", cert: leaf.CertPEM, key: leaf.KeyPEM},
		{name: "key in the certificate file", cert: append(append([]byte(nil), leaf.CertPEM...), leaf.KeyPEM...)},
		{name: "encrypted key", cert: leaf.CertPEM, key: encrypted, pass: "correct horse"},
		{name: "wrong passphrase", cert: leaf.CertPEM, key: encrypted, pass: "battery staple", failure: "the passphrase does not open the private key"},
		{name: "missing passphrase", cert: leaf.CertPEM, key: encrypted, failure: "the private key is encrypted and no passphrase was given"},
		{name: "damaged ciphertext", cert: leaf.CertPEM, key: pem.EncodeToMemory(corrupt), pass: "correct horse", failure: "the passphrase does not open the private key"},
		{name: "legacy encryption", cert: leaf.CertPEM, key: legacy, pass: "x", failure: "legacy PEM encryption is not supported; use a PKCS#8 encrypted key"},
		{name: "unsupported scheme", cert: leaf.CertPEM, key: pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x00}}), pass: "x", failure: "the private key's encryption is not PBES2 with PBKDF2 and AES-CBC"},
		{name: "mismatched key", cert: leaf.CertPEM, key: other.KeyPEM, failure: "the private key does not match the client certificate"},
		{name: "no certificate", cert: leaf.KeyPEM, key: leaf.KeyPEM, failure: "no PEM certificate in the client certificate file"},
		{name: "no key", cert: leaf.CertPEM, key: leaf.CertPEM, failure: "no PEM private key for the client certificate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pair, err := ClientCertificate(c.cert, c.key, c.pass)
			var material *TLSMaterialError
			if c.failure == "" {
				if err != nil || len(pair.Certificate) != 1 {
					t.Fatalf("identity not opened: %v", err)
				}
				return
			}
			if !errors.As(err, &material) || !material.Client || material.Reason != c.failure || c.pass != "" && strings.Contains(err.Error(), c.pass) {
				t.Fatalf("got %v, want %q", err, c.failure)
			}
		})
	}
	if _, err := TrustPool([]byte("not a bundle")); err == nil {
		t.Fatal("a bundle without certificates was trusted")
	}
}

// Trust and the client identity reach the TLS handshake of the request that
// named them, and the observation records what the hop did.
func TestDoAppliesRequestTLSAndObservesTheHop(t *testing.T) {
	serverCA, clientCA := tlstest.NewCA(t, "Server CA"), tlstest.NewCA(t, "Client CA")
	client := clientCA.Client(t, "deploy-bot")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "peer="+r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCA.Server(t, "app.example.test").TLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCA.Pool()}
	server.StartTLS()
	t.Cleanup(server.Close)
	port := netip.MustParseAddrPort(server.Listener.Addr().String()).Port()
	roots, err := TrustPool(serverCA.PEM)
	testutil.FailErr(t, "trust pool", err)
	pair, err := ClientCertificate(client.CertPEM, client.KeyPEM, "")
	testutil.FailErr(t, "client identity", err)
	request := Request{
		Class: egressclass.AgentHTTPRequest, URL: "https://app.example.test/v1?x=1", OriginHeaders: []Header{{Name: "X-Probe", Value: "1"}},
		Resolve:      []ResolveMapping{{Host: "app.example.test", Port: 443, TargetHost: "127.0.0.1", TargetPort: port}},
		TLS:          &ClientTLS{Roots: roots, Certificates: []tls.Certificate{pair}},
		AllowAddress: func(addr netip.Addr, p uint16) bool { return addr.IsLoopback() && p == port },
	}
	resp, err := Do(t.Context(), request)
	testutil.FailErr(t, "mutual TLS exchange", err)
	o := resp.Observed
	if string(resp.Body) != "peer=deploy-bot" || o == nil || o.Remote != netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port) || o.Method != "GET" || o.RequestTarget != "/v1?x=1" {
		t.Fatalf("exchange %q observed %+v", resp.Body, o)
	}
	sent := map[string]string{}
	for _, h := range o.Sent {
		sent[h.Name] = h.Value
	}
	if sent["Host"] != "app.example.test" || sent["X-Probe"] != "1" || o.TLS == nil || o.TLS.PeerCertificate.Subject != "CN=app.example.test" {
		t.Fatalf("sent %v tls %+v", sent, o.TLS)
	}
	if o.NameLookup > o.Connect || o.Connect > o.AppConnect || o.AppConnect > o.StartTransfer || o.StartTransfer > resp.Elapsed {
		t.Fatalf("phases out of order: %+v elapsed %v", o, resp.Elapsed)
	}
	request.TLS = nil
	var fault *TLSFault
	if _, err := Do(t.Context(), request); !errors.As(err, &fault) || fault.Type != "unknown_authority" && fault.Type != "untrusted" {
		t.Fatalf("the test CA was trusted without the request naming it: %v", err)
	}
}

func TestMappedDialMatchesConnectToForms(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	mappings := []ResolveMapping{
		{Host: "api.test", Port: 443, TargetHost: "backend.test", TargetPort: 8443},
		{Port: 80, Address: loopback},
		{Host: "any-port.test", TargetHost: "elsewhere.test"},
	}
	for _, c := range []struct {
		host string
		port uint16
		want DialTarget
		ok   bool
	}{
		{"api.test", 443, DialTarget{Host: "backend.test", Port: 8443}, true},
		{"API.TEST", 443, DialTarget{Host: "backend.test", Port: 8443}, true},
		{"api.test", 8080, DialTarget{}, false},
		{"whatever.test", 80, DialTarget{Address: loopback, Host: "whatever.test", Port: 80}, true},
		{"any-port.test", 9000, DialTarget{Host: "elsewhere.test", Port: 9000}, true},
	} {
		got, ok := MappedDial(mappings, c.host, c.port)
		if ok != c.ok || got != c.want {
			t.Fatalf("%s:%d mapped to %+v %v, want %+v", c.host, c.port, got, ok, c.want)
		}
	}
	// A named target passes the same address policy as the URL host.
	in := Request{Resolve: []ResolveMapping{{Host: "api.test", TargetHost: "localhost", TargetPort: 9}}}
	target, err := url.Parse("https://api.test/")
	testutil.FailErr(t, "parse url", err)
	if _, _, err := in.transportForHop(t.Context(), target); err == nil {
		t.Fatal("a loopback connect-to target dialed without loopback authority")
	}
}

// A platform verifier's own rejection is still a typed verification fault.
func TestClassifyTLSErrorNamesPlatformRejections(t *testing.T) {
	leaf := tlstest.NewCA(t, "Server CA").Server(t, "app.example.test")
	cert, err := x509.ParseCertificate(leaf.TLS.Certificate[0])
	testutil.FailErr(t, "parse leaf", err)
	fault := ClassifyTLSError(&tls.CertificateVerificationError{UnverifiedCertificates: []*x509.Certificate{cert}, Err: errors.New("certificate is not trusted")})
	if fault == nil || fault.Type != "untrusted" || fault.Subject != "CN=app.example.test" || fault.Issuer != "CN=Server CA" || fault.Reason != "certificate is not trusted" {
		t.Fatalf("platform rejection classified as %+v", fault)
	}
}
