//go:build linux

package systemproxy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

const (
	desktopSettingsTimeout = 2 * time.Second
	// desktopSettingsTTL bounds how long a desktop change stays unseen.
	desktopSettingsTTL = 15 * time.Second
)

// Desktop configuration is consulted in a fixed order: the GNOME schema, then
// KDE's file. The first source declaring a proxy mode wins.
var desktopSources = []func(context.Context) (desktopConfiguration, bool, error){
	gsettingsConfiguration,
	func(context.Context) (desktopConfiguration, bool, error) { return readKIOConfiguration(os.LookupEnv) },
}

type desktopCache struct {
	mu     sync.Mutex
	at     time.Time
	config desktopConfiguration
	err    error
}

var cache desktopCache

// Lookup resolves the desktop-configured proxy for target.
func Lookup(target *url.URL) (*url.URL, error) {
	if target == nil {
		return nil, errors.New("system proxy target is required")
	}
	config, err := cache.current(context.Background())
	if err != nil {
		return nil, err
	}
	return config.proxyFor(target)
}

func (c *desktopCache) current(ctx context.Context) (desktopConfiguration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < desktopSettingsTTL {
		return c.config, c.err
	}
	c.config, c.err = resolveDesktopConfiguration(ctx, desktopSources)
	c.at = time.Now()
	return c.config, c.err
}

func resolveDesktopConfiguration(ctx context.Context, sources []func(context.Context) (desktopConfiguration, bool, error)) (desktopConfiguration, error) {
	for _, source := range sources {
		config, ok, err := source(ctx)
		if err != nil {
			return desktopConfiguration{}, err
		}
		if ok && config.Mode != modeNone {
			return config, nil
		}
	}
	return desktopConfiguration{Source: SourceNone, Mode: modeNone}, nil
}

var runGSettings = func(ctx context.Context, program string) ([]byte, int, error) {
	return execpkg.Run(ctx, program, []string{"list-recursively", gsettingsSchema}, execpkg.ExecOpts{
		Launch:         execpkg.HostLaunch("desktop_proxy_settings"),
		Timeout:        desktopSettingsTimeout,
		MaxOutputBytes: maxGSettingsBytes,
		AppendEnv:      []string{"LC_ALL=C"},
	})
}

// gsettingsConfiguration reads the GNOME proxy schema through the desktop's
// own settings tool; a desktop without it has no GNOME proxy configuration.
func gsettingsConfiguration(ctx context.Context) (desktopConfiguration, bool, error) {
	program, err := execpkg.LookPathIn("gsettings", "/usr/bin:/bin")
	if err != nil {
		return desktopConfiguration{}, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, desktopSettingsTimeout)
	defer cancel()
	out, code, err := runGSettings(ctx, program)
	if err != nil {
		return desktopConfiguration{}, false, fmt.Errorf("read GNOME proxy settings: %w", err)
	}
	if code != 0 {
		// The schema is absent on desktops that do not ship GNOME settings.
		return desktopConfiguration{}, false, nil
	}
	config, err := parseGSettingsDump(out)
	if err != nil {
		return desktopConfiguration{}, false, fmt.Errorf("read GNOME proxy settings: %w", err)
	}
	return config, true, nil
}
