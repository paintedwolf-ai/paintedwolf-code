//go:build darwin && cgo

package systemproxy

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPACScriptResolution(t *testing.T) {
	target, err := url.Parse("https://api.example.com/v1/models")
	testutil.FailErr(t, "parse target", err)
	proxy, err := lookupPACScript(`function FindProxyForURL(url, host) { return "PROXY 127.0.0.1:8123"; }`, target)
	testutil.FailErr(t, "resolve PAC", err)
	if proxy == nil || proxy.String() != "http://127.0.0.1:8123" {
		t.Fatalf("proxy = %v", proxy)
	}
}

func TestPACScriptCanSelectDirect(t *testing.T) {
	target, err := url.Parse("https://local.example/")
	testutil.FailErr(t, "parse target", err)
	proxy, err := lookupPACScript(`function FindProxyForURL(url, host) { return "DIRECT"; }`, target)
	testutil.FailErr(t, "resolve PAC", err)
	if proxy != nil {
		t.Fatalf("proxy = %v, want direct", proxy)
	}
}
