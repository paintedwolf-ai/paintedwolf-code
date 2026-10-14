package systemproxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// kioConfigurationPath follows the KDE configuration lookup for the user file.
func kioConfigurationPath(lookupEnv func(string) (string, bool)) string {
	if dir, ok := lookupEnv("XDG_CONFIG_HOME"); ok && filepath.IsAbs(dir) {
		return filepath.Join(dir, "kioslaverc")
	}
	home, ok := lookupEnv("HOME")
	if !ok || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".config", "kioslaverc")
}

// readKIOConfiguration reports whether a proxy group exists in the user's
// kioslaverc. Credentials for KDE proxies live in the wallet and are not read.
func readKIOConfiguration(lookupEnv func(string) (string, bool)) (desktopConfiguration, bool, error) {
	path := kioConfigurationPath(lookupEnv)
	if path == "" {
		return desktopConfiguration{}, false, nil
	}
	file, err := os.Open(path) //nolint:gosec // G304 — fixed basename under the user's configuration directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return desktopConfiguration{}, false, nil
		}
		return desktopConfiguration{}, false, fmt.Errorf("read KDE proxy configuration: %w", err)
	}
	defer func() { _ = file.Close() }()
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(bufio.NewReader(&boundedReader{reader: file, remaining: maxKIOBytes + 1})); err != nil {
		return desktopConfiguration{}, false, fmt.Errorf("read KDE proxy configuration: %w", err)
	}
	if raw.Len() > maxKIOBytes {
		return desktopConfiguration{}, false, errors.New("KDE proxy configuration exceeds its size limit")
	}
	group, ok := iniGroup(raw.String(), kioProxyGroup)
	if !ok {
		return desktopConfiguration{}, false, nil
	}
	config, err := parseKIOProxyGroup(group, lookupEnv)
	return config, err == nil, err
}

func parseKIOProxyGroup(group map[string]string, lookupEnv func(string) (string, bool)) (desktopConfiguration, error) {
	config := desktopConfiguration{Source: SourceKIO}
	proxyType := kioProxyNone
	if raw, ok := group["ProxyType"]; ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return desktopConfiguration{}, fmt.Errorf("KDE proxy type %q is not a number", raw)
		}
		proxyType = parsed
	}
	switch proxyType {
	case kioProxyNone:
		config.Mode = modeNone
		return config, nil
	case kioProxyScript:
		config.Mode, config.Script = modeAuto, strings.TrimSpace(group["Proxy Config Script"])
		return config, nil
	case kioProxyDiscovery:
		config.Mode = modeAuto
		return config, nil
	case kioProxyManual, kioProxyEnvironment:
	default:
		return desktopConfiguration{}, fmt.Errorf("KDE proxy type %d is unknown", proxyType)
	}
	config.Mode = modeManual
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
		return desktopConfiguration{}, err
	}
	if config.HTTPS, err = kioProxyURL(value("httpsProxy"), "http"); err != nil {
		return desktopConfiguration{}, err
	}
	if config.SOCKS, err = kioProxyURL(value("socksProxy"), "socks5"); err != nil {
		return desktopConfiguration{}, err
	}
	if exceptions := value("NoProxyFor"); exceptions != "" {
		for _, entry := range strings.Split(exceptions, ",") {
			if entry = strings.TrimSpace(entry); entry != "" {
				config.Bypass = append(config.Bypass, entry)
			}
		}
	}
	config.BypassInverted = strings.EqualFold(strings.TrimSpace(group["ReversedException"]), "true")
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

// iniGroup returns one group's keys; locale and flag suffixes such as
// `key[$e]` are stripped so configuration written by KDE tooling matches.
func iniGroup(raw, name string) (map[string]string, bool) {
	group := map[string]string{}
	found, inside := false, false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			inside = strings.TrimSuffix(line[1:], "]") == name
			found = found || inside
			continue
		}
		if !inside {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if bracket := strings.IndexByte(key, '['); bracket >= 0 {
			key = key[:bracket]
		}
		group[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return group, found
}

type boundedReader struct {
	reader    *os.File
	remaining int
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, errors.New("KDE proxy configuration exceeds its size limit")
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= n
	return n, err
}
