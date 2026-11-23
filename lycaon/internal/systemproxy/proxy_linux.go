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

type desktopSource func(context.Context) (Configuration, bool, error)

type desktopCache struct {
	mu      sync.Mutex
	at      time.Time
	config  Configuration
	err     error
	sources []desktopSource
}

var cache = desktopCache{sources: []desktopSource{
	func(ctx context.Context) (Configuration, bool, error) {
		return gsettingsConfiguration(ctx, hostread.DesktopProxySettings)
	},
	func(context.Context) (Configuration, bool, error) { return readKIOConfiguration(os.LookupEnv) },
}}

// Current returns the desktop's proxy configuration.
func Current() (Configuration, error) {
	return CurrentContext(context.Background())
}

// CurrentContext bounds native discovery by the caller's lifetime.
func CurrentContext(ctx context.Context) (Configuration, error) {
	return cache.current(ctx)
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
	if err := ctx.Err(); err != nil {
		return Configuration{}, err
	}
	if !c.at.IsZero() && time.Since(c.at) < desktopSettingsTTL {
		return c.config, c.err
	}
	config, err := resolveDesktopConfiguration(ctx, c.sources)
	if ctx.Err() != nil {
		return Configuration{}, ctx.Err()
	}
	c.config, c.err = config, err
	c.at = time.Now()
	return c.config, c.err
}

func resolveDesktopConfiguration(ctx context.Context, sources []desktopSource) (Configuration, error) {
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

// gsettingsConfiguration reads the GNOME proxy schema through the desktop's
// own settings tool; a desktop without it has no GNOME proxy configuration.
func gsettingsConfiguration(ctx context.Context, read func(context.Context) ([]byte, int, error)) (Configuration, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, desktopSettingsTimeout)
	defer cancel()
	out, code, err := read(ctx)
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
