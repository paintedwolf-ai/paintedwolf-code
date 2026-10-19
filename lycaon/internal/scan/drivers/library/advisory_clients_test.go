package library

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAdvisoryDownloadsApplyHostProxyToTheEffectiveDestination(t *testing.T) {
	var destinations []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinations = append(destinations, r.URL.Hostname())
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("unexpected credentials")
		}
		if r.URL.Hostname() == "first.invalid" {
			w.Header().Set("Location", "http://second.invalid/export.zip")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "fixture export")
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")
	clients := advisoryClients()
	defer clients.HTTPClient().CloseIdleConnections()
	response, err := clients.HTTPClient().Get("http://first.invalid/export.zip")
	testutil.FailErr(t, "fetch advisory through host proxy", err)
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	testutil.FailErr(t, "read advisory fixture", err)
	if string(raw) != "fixture export" || len(destinations) != 2 || destinations[0] != "first.invalid" || destinations[1] != "second.invalid" {
		t.Fatalf("advisory route = %q, %v", raw, destinations)
	}
}
