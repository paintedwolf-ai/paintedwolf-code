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

	"github.com/lycaon/lycaon/internal/exec/hostread"
)

const (
	desktopSettingsTimeout = 2 * time.Second
	// desktopSettingsTTL bounds how long a desktop change stays unseen.
	desktopSettingsTTL = 15 * time.Second
)

// Desktop configuration is consulted in a fixed order: the GNOME schema, then
// KDE's file. The first source declaring a proxy mode wins.
var desktopSources = []func(context.Context) (Configuration, bool, error){
	gsettingsConfiguration,
	func(context.Context) (Configuration, bool, error) { return readKIOConfiguration(os.LookupEnv) },
}

type desktopCache struct {
	mu     sync.Mutex
	at     time.Time
	config Configuration
	err    error
}

var cache desktopCache

// Current returns the desktop's proxy configuration.
func Current() (Configuration, error) {
	return cache.current(context.Background())
}

// Lookup resolves the desktop-configured proxy for target.
func Lookup(target *url.URL) (*url.URL, error) {
	if target == nil {
		return nil, errors.New("system proxy target is required")
	}
	config, err := Current()
	if err != nil {
		return nil, err
	}
	return config.ProxyFor(target)
}

func (c *desktopCache) current(ctx context.Context) (Configuration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < desktopSettingsTTL {
		return c.config, c.err
	}
	c.config, c.err = resolveDesktopConfiguration(ctx, desktopSources)
	c.at = time.Now()
	return c.config, c.err
}

func resolveDesktopConfiguration(ctx context.Context, sources []func(context.Context) (Configuration, bool, error)) (Configuration, error) {
	for _, source := range sources {
		config, ok, err := source(ctx)
		if err != nil {
			return Configuration{}, err
		}
		if ok && config.Mode != ModeNone {
			return config, nil
		}
	}
	return Configuration{Source: SourceNone, Mode: ModeNone}, nil
}

var runGSettings = hostread.DesktopProxySettings

// gsettingsConfiguration reads the GNOME proxy schema through the desktop's
// own settings tool; a desktop without it has no GNOME proxy configuration.
func gsettingsConfiguration(ctx context.Context) (Configuration, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, desktopSettingsTimeout)
	defer cancel()
	out, code, err := runGSettings(ctx)
	if err != nil {
		return Configuration{}, false, fmt.Errorf("read GNOME proxy settings: %w", err)
	}
	if code != 0 {
		// The schema is absent on desktops that do not ship GNOME settings.
		return Configuration{}, false, nil
	}
	config, err := parseGSettingsDump(out)
	if err != nil {
		return Configuration{}, false, fmt.Errorf("read GNOME proxy settings: %w", err)
	}
	return config, true, nil
}
