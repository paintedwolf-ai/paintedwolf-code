// Package hostproxy owns environment and desktop proxy precedence.
package hostproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/systemproxy"
)

var systemConfiguration = systemproxy.CurrentContext

// Configuration is the proxy policy every host client applies: loopback
// is always direct, NO_PROXY exempts destinations, an explicit environment
// proxy serves its scheme, and the system configuration serves the rest.
type Configuration struct {
	// EnvironmentHTTP and EnvironmentHTTPS are HTTP_PROXY and HTTPS_PROXY.
	EnvironmentHTTP     *url.URL
	EnvironmentHTTPS    *url.URL
	EnvironmentHTTPErr  error
	EnvironmentHTTPSErr error
	// EnvironmentExceptions are the NO_PROXY entries; they apply to every proxy.
	EnvironmentExceptions []systemproxy.Exception
	System                systemproxy.Configuration
	// SystemErr is why the system configuration could not be read; it fails
	// only resolutions that need the system configuration.
	SystemErr error
}

func environmentConfiguration() Configuration {
	config := Configuration{
		EnvironmentExceptions: parseEnvironmentExceptions(environmentValue("NO_PROXY", "no_proxy")),
	}
	config.EnvironmentHTTPS, config.EnvironmentHTTPSErr = parseEnvironmentProxy(environmentValue("HTTPS_PROXY", "https_proxy"))
	// HTTP_PROXY may be attacker-controlled in a CGI request.
	if os.Getenv("REQUEST_METHOD") == "" {
		config.EnvironmentHTTP, config.EnvironmentHTTPErr = parseEnvironmentProxy(environmentValue("HTTP_PROXY", "http_proxy"))
	}
	return config
}

// Current reads both sources when exporting policy for multiple schemes.
func Current() Configuration {
	config := environmentConfiguration()
	config.System, config.SystemErr = systemConfiguration(context.Background())
	return config
}

// ExplicitProxy is the environment proxy for scheme, or nil when the system
// configuration decides.
func (p Configuration) ExplicitProxy(scheme string) *url.URL {
	switch strings.ToLower(scheme) {
	case "https":
		return p.EnvironmentHTTPS
	case "http":
		return p.EnvironmentHTTP
	default:
		return nil
	}
}

// ExplicitProxyError reports a declared environment policy that could not be read.
func (p Configuration) ExplicitProxyError(scheme string) error {
	switch strings.ToLower(scheme) {
	case "https":
		return p.EnvironmentHTTPSErr
	case "http":
		return p.EnvironmentHTTPErr
	default:
		return nil
	}
}

func (p Configuration) explicit(target *url.URL) (*url.URL, bool, error) {
	if loopbackDestination(target) {
		return nil, true, nil
	}
	host, port := systemproxy.TargetHostPort(target)
	for _, exception := range p.EnvironmentExceptions {
		if exception.Matches(host, port) {
			return nil, true, nil
		}
	}
	if err := p.ExplicitProxyError(target.Scheme); err != nil {
		return nil, true, err
	}
	if proxy := p.ExplicitProxy(target.Scheme); proxy != nil {
		return proxy, true, nil
	}
	return nil, false, nil
}

// ProxyFor applies policy to the original destination.
func (p Configuration) ProxyFor(target *url.URL) (*url.URL, error) {
	if proxy, decided, err := p.explicit(target); decided {
		return proxy, err
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

// ForRequest resolves policy for the effective HTTP destination.
func ForRequest(request *http.Request) (*url.URL, error) {
	if request == nil || request.URL == nil {
		return nil, nil
	}
	return ResolveContext(request.Context(), request.URL)
}

// Resolve reports the proxy every host client would use for target.
func Resolve(target *url.URL) (*url.URL, error) {
	return ResolveContext(context.Background(), target)
}

// ResolveContext reads desktop state only when environment policy needs it.
func ResolveContext(ctx context.Context, target *url.URL) (*url.URL, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("proxy resolution requires a target")
	}
	config := environmentConfiguration()
	if proxy, decided, err := config.explicit(target); decided {
		return proxy, err
	}
	config.System, config.SystemErr = systemConfiguration(ctx)
	return config.ProxyFor(target)
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
func parseEnvironmentProxy(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Hostname() == "" || !environmentProxyScheme(proxy.Scheme) || proxy.Path != "" && proxy.Path != "/" || proxy.RawQuery != "" || proxy.Fragment != "" {
		return nil, fmt.Errorf("the configured environment proxy is invalid")
	}
	return proxy, nil
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
