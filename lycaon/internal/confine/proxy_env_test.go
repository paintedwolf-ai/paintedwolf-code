package confine_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func envValue(entries []string, key string) (string, bool) {
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value, true
		}
	}
	return "", false
}

// The opt-in travels with the variables it turns on.
func TestProxyEnvCarriesRuntimeOptIn(t *testing.T) {
	env := confine.ProxyEnv(confine.Confinement{ProxyAddr: "127.0.0.1:9"})
	if _, ok := envValue(env, "HTTPS_PROXY"); !ok {
		t.Fatalf("proxy env lost HTTPS_PROXY: %v", env)
	}
	value, ok := envValue(env, "NODE_USE_ENV_PROXY")
	if !ok {
		t.Fatalf("proxy env omitted the Node opt-in: %v", env)
	}
	if value != "1" {
		t.Fatalf("NODE_USE_ENV_PROXY = %q want 1", value)
	}
}

func TestProxyEnvEmptyWithoutProxyAddress(t *testing.T) {
	if env := confine.ProxyEnv(confine.Confinement{}); len(env) != 0 {
		t.Fatalf("proxy env without an address = %v", env)
	}
}

// Left standing under direct_ip or deny, an opt-in points a runtime at a front
// door this spawn does not have.
func TestWithoutProxyEnvStripsRuntimeOptIn(t *testing.T) {
	base := append([]string{"PATH=/bin", "HOME=/tmp"},
		confine.ProxyEnv(confine.Confinement{ProxyAddr: "127.0.0.1:9"})...)
	got := confine.WithoutProxyEnv(base)
	for _, key := range confine.ProxyEnvKeys() {
		if _, present := envValue(got, key); present {
			t.Fatalf("stripped environment retained %s: %v", key, got)
		}
	}
	if _, ok := envValue(got, "PATH"); !ok {
		t.Fatalf("stripped environment lost PATH: %v", got)
	}
}

// Other launchers build their baseline from ProxyEnvKeys, so it has to cover
// every name ProxyEnv emits.
func TestProxyEnvKeysCoverEveryEmittedName(t *testing.T) {
	keys := make(map[string]struct{})
	for _, key := range confine.ProxyEnvKeys() {
		keys[key] = struct{}{}
	}
	env := confine.ProxyEnv(confine.Confinement{
		ProxyAddr:      "127.0.0.1:9",
		SocksProxyEnv:  true,
		SocksProxyAddr: "127.0.0.1:10",
	})
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := keys[name]; !ok {
			t.Fatalf("ProxyEnv emits %s but ProxyEnvKeys omits it", name)
		}
	}
}

// Build caches key on the environment, so the same boundary must produce the
// same bytes on every call. A per-action value here is a cache miss in every
// project the agent touches.
func TestProxyEnvIsIdenticalAcrossInvocations(t *testing.T) {
	boundary := confine.Confinement{
		ProxyAddr: "127.0.0.1:9", SocksProxyEnv: true, SocksProxyAddr: "127.0.0.1:10",
	}
	first := strings.Join(confine.ProxyEnv(boundary), "\n")
	second := strings.Join(confine.ProxyEnv(boundary), "\n")
	if first != second {
		t.Fatalf("proxy env differs between invocations:\n%s\n---\n%s", first, second)
	}
	for _, secret := range []string{"@", "password", ":x@"} {
		if strings.Contains(first, secret) {
			t.Fatalf("proxy env carries a credential (%q):\n%s", secret, first)
		}
	}
}
