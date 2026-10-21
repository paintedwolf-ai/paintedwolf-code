package systemproxy

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	kioProxyGroup = "Proxy Settings"
	maxKIOBytes   = 256 << 10
)

// KDE proxy types as kioslaverc stores them (KProtocolManager::ProxyType).
const (
	kioProxyNone        = 0
	kioProxyManual      = 1
	kioProxyScript      = 2
	kioProxyDiscovery   = 3
	kioProxyEnvironment = 4
)

func parseKIOProxyGroup(group map[string]string, lookupEnv func(string) (string, bool)) (Configuration, error) {
	config := Configuration{Source: SourceKIO}
	proxyType := kioProxyNone
	if raw, ok := group["ProxyType"]; ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return Configuration{}, fmt.Errorf("KDE proxy type %q is not a number", raw)
		}
		proxyType = parsed
	}
	switch proxyType {
	case kioProxyNone:
		config.Mode = ModeNone
		return config, nil
	case kioProxyScript:
		config.Mode, config.Script = ModeAuto, strings.TrimSpace(group["Proxy Config Script"])
		return config, nil
	case kioProxyDiscovery:
		config.Mode = ModeAuto
		return config, nil
	case kioProxyManual, kioProxyEnvironment:
	default:
		return Configuration{}, fmt.Errorf("KDE proxy type %d is unknown", proxyType)
	}
	config.Mode = ModeManual
	value := func(key string) string {
		raw := strings.TrimSpace(group[key])
		if proxyType != kioProxyEnvironment || raw == "" {
			return raw
		}
		resolved, _ := lookupEnv(raw)
		return strings.TrimSpace(resolved)
	}
	var err error
	if config.HTTP, err = kioProxyURL(value("httpProxy"), "http"); err != nil {
		return Configuration{}, err
	}
	if config.HTTPS, err = kioProxyURL(value("httpsProxy"), "http"); err != nil {
		return Configuration{}, err
	}
	if config.SOCKS, err = kioProxyURL(value("socksProxy"), "socks5"); err != nil {
		return Configuration{}, err
	}
	if exceptions := value("NoProxyFor"); exceptions != "" {
		config.Exceptions = desktopExceptions(strings.Split(exceptions, ","))
	}
	config.ExceptionsInverted = strings.EqualFold(strings.TrimSpace(group["ReversedException"]), "true")
	return config, nil
}

// kioProxyURL accepts the `scheme://host port`, `scheme://host:port` and
// `host:port` spellings KDE writes.
func kioProxyURL(raw, defaultScheme string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if host, port, ok := strings.Cut(raw, " "); ok {
		if _, err := strconv.Atoi(strings.TrimSpace(port)); err != nil {
			return nil, fmt.Errorf("KDE proxy %q has an invalid port", raw)
		}
		raw = strings.TrimSpace(host) + ":" + strings.TrimSpace(port)
	}
	if !strings.Contains(raw, "://") {
		raw = defaultScheme + "://" + raw
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Hostname() == "" || proxy.Port() == "" {
		return nil, fmt.Errorf("KDE proxy %q is not a host and port", raw)
	}
	switch proxy.Scheme {
	case "http", "https", "socks", "socks5", "socks5h", "socks4":
	default:
		return nil, fmt.Errorf("KDE proxy %q uses an unsupported scheme", raw)
	}
	if proxy.Scheme == "socks" {
		proxy.Scheme = "socks5"
	}
	return &url.URL{Scheme: proxy.Scheme, Host: proxy.Host, User: proxy.User}, nil
}
