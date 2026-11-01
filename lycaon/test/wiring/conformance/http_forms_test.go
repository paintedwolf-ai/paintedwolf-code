package conformance_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/tlstest"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

var httpFormContracts = []contractCase{{
	Case: commandconformance.Case{
		ID: "http-curl-transport-forms", Commands: []string{"curl"},
		Forms: []string{"curl:-v", "curl:--fail-with-body", "curl:-r", "curl:-T", "curl:--cacert", "curl:--capath", "curl:-E", "curl:--key", "curl:--cert-type", "curl:--pass", "curl:--resolve", "curl:--connect-to"},
	},
	Prepare: prepareCurlTransportForms,
	Stdout:  "234\nuploaded\nfailure-body\nstatus:22\n",
}}

func prepareCurlTransportForms(t *testing.T, c commandconformance.Case) commandconformance.Case {
	t.Helper()
	ca := tlstest.NewCA(t, "Conformance test CA")
	serverIdentity := ca.Server(t, "curl.example.test", "127.0.0.1")
	client := ca.Client(t, "conformance client")
	type exchange struct{ method, path, host, peer, body string }
	var mu sync.Mutex
	var exchanges []exchange
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		peer := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			peer = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		mu.Lock()
		exchanges = append(exchanges, exchange{r.Method, r.URL.Path, r.Host, peer, string(body)})
		mu.Unlock()
		switch r.URL.Path {
		case "/range":
			http.ServeContent(w, r, "digits.txt", referenceuserland.SeedTime, strings.NewReader("0123456789"))
		case "/upload":
			_, _ = io.WriteString(w, "uploaded\n")
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "failure-body\n")
		default:
			http.NotFound(w, r)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverIdentity.TLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.Pool()}
	server.StartTLS()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() {
		server.Close()
		mu.Lock()
		defer mu.Unlock()
		if len(exchanges) != 3 {
			t.Errorf("HTTP exchanges = %+v", exchanges)
			return
		}
		for _, x := range exchanges {
			if x.peer != "conformance client" || x.host != "curl.example.test:"+strconv.Itoa(port) {
				t.Errorf("request identity = %+v", x)
			}
		}
		if exchanges[1].method != http.MethodPut || exchanges[1].body != "payload\n" {
			t.Errorf("upload = %+v", exchanges[1])
		}
	})
	c.Files = map[string]referenceuserland.Entry{
		"ca.pem": {Data: ca.PEM}, "certs/ca.pem": {Data: ca.PEM},
		"client.pem":     {Data: client.CertPEM},
		"client-key.pem": {Data: tlstest.EncryptPKCS8(t, client.KeyDER, "fixture-passphrase"), Mode: 0o600},
		"upload.txt":     {Data: []byte("payload\n")},
	}
	common := fmt.Sprintf("curl -s --cacert ca.pem --capath certs -E client.pem --key client-key.pem --cert-type PEM --pass fixture-passphrase --resolve curl.example.test:%d:127.0.0.1", port)
	base := fmt.Sprintf("https://curl.example.test:%d", port)
	c.Program = fmt.Sprintf("%s -v -r 2-4 %s/range 2>/dev/null && printf '\\n' && %s --connect-to curl.example.test:%d:127.0.0.1:%d -T upload.txt %s/upload && %s --fail-with-body %s/error; printf 'status:%%s\\n' \"$?\"", common, base, common, port, port, base, common, base)
	c.CapabilityRequest = fmt.Sprintf(`{"loopback_connect":{"ports":[%d]}}`, port)
	return c
}
