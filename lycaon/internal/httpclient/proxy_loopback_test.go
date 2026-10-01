package httpclient

import (
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoopbackDestinationReadsEverySpelling(t *testing.T) {
	local := []string{
		"http://localhost/health",
		"http://localhost./health",
		"http://LocalHost:8787/health",
		"http://db.localhost:5432/",
		"http://127.0.0.1:8787/health",
		"http://[::1]:8787/health",
		"http://[::ffff:127.0.0.1]/health",
	}
	for _, raw := range local {
		target, err := url.Parse(raw)
		testutil.FailErr(t, "parse local url", err)
		if !loopbackDestination(target) {
			t.Errorf("loopbackDestination(%q) = false, want true", raw)
		}
	}
	remote := []string{
		"http://localhost.example.com/",
		"http://notlocalhost/",
		"https://api.example.com/",
		"http://10.0.0.5:8080/",
	}
	for _, raw := range remote {
		target, err := url.Parse(raw)
		testutil.FailErr(t, "parse remote url", err)
		if loopbackDestination(target) {
			t.Errorf("loopbackDestination(%q) = true, want false", raw)
		}
	}
}
