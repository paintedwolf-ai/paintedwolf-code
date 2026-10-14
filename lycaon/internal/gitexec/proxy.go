package gitexec

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/httpclient"
)

// proxyResolverFn is the host's proxy resolution; tests replace it.
var proxyResolverFn = httpclient.ResolveProxy

// proxyEnv gives Git the proxy every host client uses for an HTTP remote.
// Git reads only its environment and configuration, so a desktop-configured
// proxy would otherwise be bypassed. A mechanism the host refuses fails the
// invocation rather than connecting directly.
func proxyEnv(remote string) ([]string, error) {
	target, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || target.Host == "" {
		return nil, nil
	}
	scheme := strings.ToLower(target.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, nil
	}
	proxy, err := proxyResolverFn(target)
	if err != nil {
		return nil, fmt.Errorf("gitexec: %w", err)
	}
	if proxy == nil {
		return nil, nil
	}
	// libcurl prefers the lowercase name and reads only lowercase http_proxy.
	return []string{scheme + "_proxy=" + proxy.String()}, nil
}
