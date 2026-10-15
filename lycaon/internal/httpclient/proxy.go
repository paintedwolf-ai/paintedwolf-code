package httpclient

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/systemproxy"
)

var systemProxyConfiguration = systemproxy.Current

// ProxyConfiguration is the proxy policy every host client applies: loopback
// is always direct, NO_PROXY exempts destinations, an explicit environment
// proxy serves its scheme, and the system configuration serves the rest.
type ProxyConfiguration struct {
	// EnvironmentHTTP and EnvironmentHTTPS are HTTP_PROXY and HTTPS_PROXY.
	EnvironmentHTTP  *url.URL
	EnvironmentHTTPS *url.URL
	// EnvironmentExceptions are the NO_PROXY entries; they apply to every proxy.
	EnvironmentExceptions []systemproxy.Exception
	System                systemproxy.Configuration
	// SystemErr is why the system configuration could not be read; it fails
	// only resolutions that need the system configuration.
	SystemErr error
}

// CurrentProxyConfiguration reads the environment and system proxy policy.
func CurrentProxyConfiguration() ProxyConfiguration {
	config := ProxyConfiguration{
		EnvironmentExceptions: parseEnvironmentExceptions(environmentValue("NO_PROXY", "no_proxy")),
		EnvironmentHTTPS:      parseEnvironmentProxy(environmentValue("HTTPS_PROXY", "https_proxy")),
	}
	// HTTP_PROXY may be attacker-controlled in a CGI request.
	if os.Getenv("REQUEST_METHOD") == "" {
		config.EnvironmentHTTP = parseEnvironmentProxy(environmentValue("HTTP_PROXY", "http_proxy"))
	}
	config.System, config.SystemErr = systemProxyConfiguration()
	return config
}

// ExplicitProxy is the environment proxy for scheme, or nil when the system
// configuration decides.
func (p ProxyConfiguration) ExplicitProxy(scheme string) *url.URL {
	switch strings.ToLower(scheme) {
	case "https":
		return p.EnvironmentHTTPS
	case "http":
		return p.EnvironmentHTTP
	default:
		return nil
	}
}

// ProxyFor returns the proxy for target, nil for a direct connection, or why
// the system configuration cannot be honoured.
func (p ProxyConfiguration) ProxyFor(target *url.URL) (*url.URL, error) {
	if loopbackDestination(target) {
		return nil, nil
	}
	host, port := systemproxy.TargetHostPort(target)
	for _, exception := range p.EnvironmentExceptions {
		if exception.Matches(host, port) {
			return nil, nil
		}
	}
	if proxy := p.ExplicitProxy(target.Scheme); proxy != nil {
		return proxy, nil
	}
	if p.SystemErr != nil {
		return nil, fmt.Errorf("resolve system proxy: %w", p.SystemErr)
	}
	proxy, err := p.System.ProxyFor(target)
	if err != nil {
		return nil, fmt.Errorf("resolve system proxy: %w", err)
	}
	return proxy, nil
}

func hostProxy() func(*http.Request) (*url.URL, error) {
	return func(request *http.Request) (*url.URL, error) {
		if request == nil || request.URL == nil {
			return nil, nil
		}
		return CurrentProxyConfiguration().ProxyFor(request.URL)
	}
}

// ResolveProxy reports the proxy every host client would use for target.
func ResolveProxy(target *url.URL) (*url.URL, error) {
	if target == nil {
		return nil, fmt.Errorf("proxy resolution requires a target")
	}
	return CurrentProxyConfiguration().ProxyFor(target)
}

// loopbackDestination reports loopback literals and reserved local names.
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

func environmentValue(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// parseEnvironmentProxy accepts a URL or a bare host:port, which means HTTP.
func parseEnvironmentProxy(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Host == "" || !environmentProxyScheme(proxy.Scheme) {
		if proxy, err = url.Parse("http://" + raw); err != nil || proxy.Host == "" {
			return nil
		}
	}
	return proxy
}

func environmentProxyScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}

// parseEnvironmentExceptions reads NO_PROXY: `*`, address literals and CIDR
// ranges, and names with an optional port, where `name` covers the name and
// its subdomains while `.name` and `*.name` cover only the subdomains.
func parseEnvironmentExceptions(raw string) []systemproxy.Exception {
	var exceptions []systemproxy.Exception
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		exceptions = append(exceptions, parseEnvironmentException(entry))
	}
	return exceptions
}

func parseEnvironmentException(entry string) systemproxy.Exception {
	exception := systemproxy.Exception{Raw: entry, Kind: systemproxy.ExceptionInvalid}
	if entry == "*" {
		exception.Kind = systemproxy.ExceptionAll
		return exception
	}
	if _, network, err := net.ParseCIDR(entry); err == nil {
		exception.Kind, exception.Network = systemproxy.ExceptionNetwork, network
		return exception
	}
	if address := net.ParseIP(entry); address != nil {
		exception.Kind, exception.Address = systemproxy.ExceptionAddress, address
		return exception
	}
	host := entry
	if h, port, err := net.SplitHostPort(entry); err == nil {
		host, exception.Port = h, port
	}
	if address := net.ParseIP(host); address != nil {
		exception.Kind, exception.Address = systemproxy.ExceptionAddress, address
		return exception
	}
	host = strings.TrimPrefix(host, "*")
	if strings.HasPrefix(host, ".") {
		exception.Kind, exception.Domain = systemproxy.ExceptionSubdomains, strings.TrimPrefix(host, ".")
	} else {
		exception.Kind, exception.Domain = systemproxy.ExceptionDomain, host
	}
	if exception.Domain == "" {
		exception.Kind = systemproxy.ExceptionInvalid
	}
	return exception
}
