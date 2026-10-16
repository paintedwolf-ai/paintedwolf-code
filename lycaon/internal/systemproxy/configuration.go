package systemproxy

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Mode is how a proxy configuration selects proxies.
type Mode string

const (
	// ModeNone connects directly.
	ModeNone Mode = "none"
	// ModeManual names fixed proxies and an exception list.
	ModeManual Mode = "manual"
	// ModeAuto evaluates a proxy auto-configuration script or discovers one.
	ModeAuto Mode = "auto"
)

// Configuration is one source's declared proxy policy.
type Configuration struct {
	Source Source
	Mode   Mode
	// HTTP and HTTPS are the proxies for each destination scheme; SOCKS serves
	// a scheme without its own.
	HTTP  *url.URL
	HTTPS *url.URL
	SOCKS *url.URL
	// Exceptions are destinations reached directly.
	Exceptions []Exception
	// ExceptionsInverted proxies only destinations matching Exceptions.
	ExceptionsInverted bool
	// Script is the auto-configuration location; empty in auto mode means discovery.
	Script string
}

// ProxyFor returns the proxy for target, nil for a direct connection, or the
// reason the declared mechanism cannot be honoured.
func (c Configuration) ProxyFor(target *url.URL) (*url.URL, error) {
	switch c.Mode {
	case ModeAuto:
		if c.Source == SourceMacOS {
			return evaluateMacOSAutomatic(target)
		}
		mechanism := "proxy auto-configuration"
		if c.Script == "" {
			mechanism = "proxy auto-discovery"
		}
		return nil, &Unsupported{Source: c.Source, Mechanism: mechanism, Reference: c.Script}
	case ModeManual:
	default:
		return nil, nil
	}
	if c.Excepts(target) != c.ExceptionsInverted {
		return nil, nil
	}
	return c.SchemeProxy(target.Scheme), nil
}

// SchemeProxy is the proxy a destination scheme uses when not excepted.
func (c Configuration) SchemeProxy(scheme string) *url.URL {
	var proxy *url.URL
	switch strings.ToLower(scheme) {
	case "https":
		proxy = c.HTTPS
	case "http":
		proxy = c.HTTP
	}
	if proxy == nil {
		proxy = c.SOCKS
	}
	return proxy
}

// Excepts reports whether any exception entry matches target.
func (c Configuration) Excepts(target *url.URL) bool {
	host, port := TargetHostPort(target)
	for _, exception := range c.Exceptions {
		if exception.Matches(host, port) {
			return true
		}
	}
	return false
}

// TargetHostPort returns target's host and port, with the scheme's default port.
func TargetHostPort(target *url.URL) (string, string) {
	port := target.Port()
	if port == "" {
		switch strings.ToLower(target.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return target.Hostname(), port
}

// manualProxy builds one proxy endpoint; an empty host or port means none.
func manualProxy(scheme, host string, port int, user *url.Userinfo) *url.URL {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 || port > 65535 {
		return nil
	}
	return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port)), User: user}
}

func desktopExceptions(entries []string) []Exception {
	exceptions := make([]Exception, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) != "" {
			exceptions = append(exceptions, ParseDesktopException(entry))
		}
	}
	return exceptions
}
