//go:build linux

package systemproxy

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func resetCache(t *testing.T) {
	t.Helper()
	cache.mu.Lock()
	cache.at, cache.config, cache.err = time.Time{}, Configuration{}, nil
	cache.mu.Unlock()
	t.Cleanup(func() {
		cache.mu.Lock()
		cache.at, cache.config, cache.err = time.Time{}, Configuration{}, nil
		cache.mu.Unlock()
	})
}

func TestDesktopSourcesAreConsultedInOrderUntilOneDeclaresAMode(t *testing.T) {
	calls := []string{}
	sources := []func(context.Context) (Configuration, bool, error){
		func(context.Context) (Configuration, bool, error) {
			calls = append(calls, "absent")
			return Configuration{}, false, nil
		},
		func(context.Context) (Configuration, bool, error) {
			calls = append(calls, "none")
			return Configuration{Source: SourceGSettings, Mode: ModeNone}, true, nil
		},
		func(context.Context) (Configuration, bool, error) {
			calls = append(calls, "manual")
			proxy, _ := url.Parse("http://proxy.example:3128")
			return Configuration{Source: SourceKIO, Mode: ModeManual, HTTP: proxy, HTTPS: proxy}, true, nil
		},
		func(context.Context) (Configuration, bool, error) {
			t.Fatal("a later source was consulted after a declared mode")
			return Configuration{}, false, nil
		},
	}
	config, err := resolveDesktopConfiguration(t.Context(), sources)
	testutil.FailErr(t, "resolve", err)
	if config.Source != SourceKIO || len(calls) != 3 {
		t.Fatalf("configuration = %+v after %v", config, calls)
	}
	config, err = resolveDesktopConfiguration(t.Context(), sources[:2])
	testutil.FailErr(t, "resolve without declared mode", err)
	if config.Source != SourceNone || config.Mode != ModeNone {
		t.Fatalf("undeclared configuration = %+v", config)
	}
	failure := errors.New("settings unavailable")
	if _, err := resolveDesktopConfiguration(t.Context(), []func(context.Context) (Configuration, bool, error){
		func(context.Context) (Configuration, bool, error) {
			return Configuration{}, false, failure
		},
	}); !errors.Is(err, failure) {
		t.Fatalf("source failure = %v", err)
	}
}

func TestLookupCachesTheDesktopConfiguration(t *testing.T) {
	resetCache(t)
	previous := desktopSources
	t.Cleanup(func() { desktopSources = previous })
	reads := 0
	desktopSources = []func(context.Context) (Configuration, bool, error){
		func(context.Context) (Configuration, bool, error) {
			reads++
			config, err := parseGSettingsDump([]byte(gnomeManualDump))
			return config, err == nil, err
		},
	}
	for range 3 {
		proxy, err := Lookup(mustTarget(t, "https://api.example/"))
		testutil.FailErr(t, "lookup", err)
		if proxy == nil || proxy.Host != "proxy.example:3128" {
			t.Fatalf("proxy = %v", proxy)
		}
	}
	if reads != 1 {
		t.Fatalf("desktop configuration read %d times within the cache window", reads)
	}
	if _, err := Lookup(nil); err == nil {
		t.Fatal("nil target was accepted")
	}
}

func TestGSettingsToolOutcomesMapToConfigurationFacts(t *testing.T) {
	previous := runGSettings
	t.Cleanup(func() { runGSettings = previous })
	runGSettings = func(context.Context) ([]byte, int, error) { return nil, 1, nil }
	if _, ok, err := gsettingsConfiguration(t.Context()); ok || err != nil {
		t.Fatalf("absent schema = %v, %v", ok, err)
	}
	runGSettings = func(context.Context) ([]byte, int, error) { return nil, -1, errors.New("timed out") }
	if _, ok, err := gsettingsConfiguration(t.Context()); ok || err == nil {
		t.Fatalf("tool failure = %v, %v", ok, err)
	}
	runGSettings = func(context.Context) ([]byte, int, error) { return []byte("garbage\n"), 0, nil }
	if _, _, err := gsettingsConfiguration(t.Context()); err == nil {
		t.Fatal("malformed output was accepted")
	}
	runGSettings = func(context.Context) ([]byte, int, error) { return []byte(gnomeManualDump), 0, nil }
	config, ok, err := gsettingsConfiguration(t.Context())
	if err != nil || !ok || config.Mode != ModeManual {
		t.Fatalf("manual configuration = %+v, %v, %v", config, ok, err)
	}
}
