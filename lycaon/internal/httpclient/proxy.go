package httpclient

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/systemproxy"
	"golang.org/x/net/http/httpproxy"
)

var systemProxyLookup = systemproxy.Lookup

// Explicit environment proxies take precedence over system proxy selection.
func hostProxy() func(*http.Request) (*url.URL, error) {
	resolve := proxyResolver()
	return func(request *http.Request) (*url.URL, error) {
		if request == nil || request.URL == nil {
			return nil, nil
		}
		return resolve(request.URL)
	}
}

// ResolveProxy reports the proxy every host client would use for target.
func ResolveProxy(target *url.URL) (*url.URL, error) {
	if target == nil {
		return nil, fmt.Errorf("proxy resolution requires a target")
	}
	return proxyResolver()(target)
}

func proxyResolver() func(*url.URL) (*url.URL, error) {
	config := httpproxy.FromEnvironment()
	environment := config.ProxyFunc()
	return func(target *url.URL) (*url.URL, error) {
		if loopbackDestination(target) {
			return nil, nil
		}
		if explicitEnvironmentProxy(config, target.Scheme) {
			proxy, err := environment(target)
			if err != nil || proxy == nil {
				return nil, err
			}
			return proxy, nil
		}
		if environmentProxyBypass(config, target) {
			return nil, nil
		}
		proxy, err := systemProxyLookup(target)
		if err != nil {
			return nil, fmt.Errorf("resolve system proxy: %w", err)
		}
		return proxy, nil
	}
}

// Loopback literals and reserved local names bypass ambient proxies.
func loopbackDestination(target *url.URL) bool {
	host := strings.TrimSuffix(strings.TrimSpace(target.Hostname()), ".")
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	lower := strings.ToLower(host)
	return lower == "localhost" || strings.HasSuffix(lower, ".localhost")
}

func environmentProxyBypass(config *httpproxy.Config, target *url.URL) bool {
	if strings.TrimSpace(config.NoProxy) == "" {
		return false
	}
	matcher := (&httpproxy.Config{
		HTTPProxy:  "http://127.0.0.1",
		HTTPSProxy: "http://127.0.0.1",
		NoProxy:    config.NoProxy,
	}).ProxyFunc()
	proxy, err := matcher(target)
	return err == nil && proxy == nil
}

func explicitEnvironmentProxy(config *httpproxy.Config, scheme string) bool {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "http":
		return strings.TrimSpace(config.HTTPProxy) != ""
	case "https":
		return strings.TrimSpace(config.HTTPSProxy) != ""
	default:
		return false
	}
}
