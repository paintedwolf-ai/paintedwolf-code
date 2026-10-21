//go:build linux

package httpclient

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const trustChildEnv = "PW_HTTPCLIENT_TRUST_CHILD"

// TestEnvironmentTrustChild dials the URL it is given with the default client
// after optionally pinning system trust, and prints what it observed. It runs
// in its own process because crypto/x509 loads system roots once per process.
func TestEnvironmentTrustChild(t *testing.T) {
	mode := os.Getenv(trustChildEnv)
	if mode == "" {
		return
	}
	if mode == "pinned" {
		if err := PinSystemTrust(); err != nil {
			t.Fatalf("pin system trust: %v", err)
		}
	}
	resp, err := http.Get(os.Getenv("PW_HTTPCLIENT_TRUST_URL"))
	if err == nil {
		_ = resp.Body.Close()
		os.Stdout.WriteString("dial: trusted\n")
	} else {
		os.Stdout.WriteString("dial: refused\n")
	}
	os.Stdout.WriteString("SSL_CERT_FILE=" + os.Getenv("SSL_CERT_FILE") + "\n")
}

func TestPinnedTrustIgnoresEnvironmentAnchorsAndKeepsThemForChildren(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	anchor := filepath.Join(t.TempDir(), "anchor.pem")
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	testutil.FailErr(t, "write environment anchor", os.WriteFile(anchor, raw, 0o600))
	run := func(mode string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestEnvironmentTrustChild$")
		cmd.Env = append(os.Environ(), trustChildEnv+"="+mode, "PW_HTTPCLIENT_TRUST_URL="+server.URL, "SSL_CERT_FILE="+anchor, "SSL_CERT_DIR=")
		out, err := cmd.Output()
		testutil.FailErr(t, "run "+mode+" child", err)
		return string(out)
	}
	// Without pinning, the environment anchor is trusted: the fixture discriminates.
	if out := run("inherited"); !strings.Contains(out, "dial: trusted") {
		t.Fatalf("environment anchor not honored without pinning:\n%s", out)
	}
	out := run("pinned")
	if !strings.Contains(out, "dial: refused") {
		t.Fatalf("pinned process trusted an environment anchor:\n%s", out)
	}
	if !strings.Contains(out, "SSL_CERT_FILE="+anchor+"\n") {
		t.Fatalf("pinning did not restore the variable for children:\n%s", out)
	}
}

func TestPinnedTrustStillVerifiesSystemRoots(t *testing.T) {
	testutil.FailErr(t, "pin system trust", PinSystemTrust())
	pool, err := x509.SystemCertPool()
	testutil.FailErr(t, "system pool", err)
	if pool == nil || pool.Equal(x509.NewCertPool()) {
		t.Fatal("pinned system pool is empty")
	}
}
