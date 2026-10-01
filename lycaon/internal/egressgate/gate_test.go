package egressgate

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A hop's port reaches the decision, so a redirect to another port on the same
// host is decided for itself rather than riding the first hop's answer. The
// scheme supplies the port when the URL leaves it implicit.
func TestHopEndpointCarriesThePort(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.test/v1":       "api.test:443",
		"http://api.test/v1":        "api.test:80",
		"https://api.test:8443/v1":  "api.test:8443",
		"http://api.test:9200/_all": "api.test:9200",
		"http://[::1]:3000/":        "[::1]:3000",
	}
	for raw, want := range cases {
		target, err := url.Parse(raw)
		testutil.FailErr(t, "parse hop url", err)
		if got := hopEndpoint(target); got != want {
			t.Errorf("hopEndpoint(%q) = %q, want %q", raw, got, want)
		}
	}
	empty, err := url.Parse("/relative")
	testutil.FailErr(t, "parse relative url", err)
	if got := hopEndpoint(empty); got != "" {
		t.Errorf("hopEndpoint with no host = %q, want empty", got)
	}
}
