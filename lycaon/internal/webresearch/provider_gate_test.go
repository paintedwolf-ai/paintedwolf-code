package webresearch

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webindex"
)

type memQuotaStore struct {
	mu    sync.Mutex
	count map[string]int
}

func (m *memQuotaStore) ProviderQuotaCount(_ context.Context, providerID, utcDay string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.count == nil {
		return 0, nil
	}
	return m.count[providerID+":"+utcDay], nil
}

func (m *memQuotaStore) ReserveProviderQuota(_ context.Context, providerID, utcDay string, cap int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.count == nil {
		m.count = make(map[string]int)
	}
	key := providerID + ":" + utcDay
	if m.count[key] >= cap {
		return false, nil
	}
	m.count[key]++
	return true, nil
}

func TestProviderGateMinInterval(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	clock := now
	g := newProviderGate("mwmbl", &PacingSpec{MinIntervalMS: 1000}, &memQuotaStore{}, func() time.Time { return clock })
	if err := g.acquire(context.Background(), false); err != nil {
		testutil.FailErr(t, "first acquire", err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if err := g.acquire(context.Background(), false); err == nil {
		t.Fatal("expected paced on second immediate acquire")
	}
	clock = clock.Add(600 * time.Millisecond)
	if err := g.acquire(context.Background(), false); err != nil {
		testutil.FailErr(t, "acquire after interval", err)
	}
}

func TestProviderGateDailyCapPersisted(t *testing.T) {
	path := t.TempDir() + "/web-index.db"
	store, err := webindex.Open(t.Context(), path)
	testutil.FailErr(t, "Open index", err)
	defer func() { _ = store.Close() }()

	day := "2026-07-04"
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	g := newProviderGate("mwmbl", &PacingSpec{DailyCap: 2}, store, func() time.Time { return now })
	ctx := context.Background()
	if err := g.acquire(ctx, false); err != nil {
		testutil.FailErr(t, "acquire 1", err)
	}
	if err := g.acquire(ctx, false); err != nil {
		testutil.FailErr(t, "acquire 2", err)
	}
	if err := g.acquire(ctx, false); err == nil {
		t.Fatal("expected daily cap skip")
	}

	store2, err := webindex.Open(t.Context(), path)
	testutil.FailErr(t, "reopen index", err)
	defer func() { _ = store2.Close() }()
	count, err := store2.ProviderQuotaCount(ctx, "mwmbl", day)
	testutil.FailErr(t, "ProviderQuotaCount", err)
	if count != 2 {
		t.Fatalf("count = %d want 2", count)
	}
}

func TestProviderGate429Cooldown(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	clock := now
	g := newProviderGate("mwmbl", &PacingSpec{Cooldown429S: 60}, &memQuotaStore{}, func() time.Time { return clock })
	g.record(429)
	if err := g.acquire(context.Background(), false); err == nil {
		t.Fatal("expected cooldown skip")
	}
	clock = clock.Add(61 * time.Second)
	if err := g.acquire(context.Background(), false); err != nil {
		testutil.FailErr(t, "acquire after cooldown", err)
	}
}

func TestProviderGateResetCooldownPreservesDurableQuota(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	quota := &memQuotaStore{}
	g := newProviderGate("mwmbl", &PacingSpec{DailyCap: 2, Cooldown429S: 60}, quota, func() time.Time { return now })
	testutil.FailErr(t, "first acquire", g.acquire(context.Background(), false))
	g.record(429)
	g.resetCooldown()
	testutil.FailErr(t, "acquire after repair", g.acquire(context.Background(), false))
	if err := g.acquire(context.Background(), false); !errors.Is(err, ErrProviderPaced) {
		t.Fatalf("third acquire = %v want durable daily quota to remain spent", err)
	}
}

func TestProviderGateWaitAllowanceQueues(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	g := newProviderGate("mwmbl", &PacingSpec{MinIntervalMS: 100}, &memQuotaStore{}, func() time.Time { return now })
	ctx := context.Background()
	testutil.FailErr(t, "first acquire", g.acquire(ctx, false))

	// With a wait allowance covering the pacing gap, the second call queues
	// for its reserved slot instead of skipping.
	start := time.Now()
	testutil.FailErr(t, "queued acquire", g.acquire(withProviderGateWait(ctx, time.Second), false))
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("queued acquire returned in %v — did not wait for the pacing slot", elapsed)
	}

	// Without an allowance the next call (slot now two intervals out) skips.
	if err := g.acquire(ctx, false); err == nil {
		t.Fatal("expected paced skip without wait allowance")
	}
}

func TestProviderGateWaitAllowanceTooSmallSkips(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	g := newProviderGate("github", &PacingSpec{MinIntervalMS: 6000}, &memQuotaStore{}, func() time.Time { return now })
	ctx := context.Background()
	testutil.FailErr(t, "first acquire", g.acquire(ctx, false))
	if err := g.acquire(withProviderGateWait(ctx, time.Second), false); err == nil {
		t.Fatal("expected paced skip when the slot is beyond the allowance")
	}
}

func TestProviderGateAuthenticatedBypass(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	g := newProviderGate("github", &PacingSpec{MinIntervalMS: 60_000}, &memQuotaStore{}, func() time.Time { return now })
	entry := CatalogEntry{ID: "github", OptionalCredentialSlot: "github"}
	s := Settings{Keys: map[string]string{"github": "token"}}
	if err := g.acquire(context.Background(), providerAuthenticated(entry, s)); err != nil {
		testutil.FailErr(t, "authenticated acquire", err)
	}
	if err := g.acquire(context.Background(), providerAuthenticated(entry, s)); err != nil {
		testutil.FailErr(t, "authenticated acquire again", err)
	}
}

func TestMwmblConfiguredWithoutSettings(t *testing.T) {
	reg := testRegistry(t)
	p := reg.Get("mwmbl")
	if p == nil {
		t.Fatal("missing mwmbl provider")
	}
	if p.Kind() != KindKeyless {
		t.Fatalf("kind = %q want keyless", p.Kind())
	}
	if !p.Configured(Settings{}) {
		t.Fatal("keyless mwmbl should be configured with zero settings")
	}
}

func TestCredentialSourceDistinguishesStoredAndEnvironment(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: github
    kind: keyless
    label: GitHub
    hint: GitHub
    test_query: test
    default_endpoint: https://api.github.com
    optional_credential_slot: github
    api_key_env: GITHUB_SEARCH_API_KEY
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "load catalog", err)
	creds := NewCredentialStoreAt(filepath.Join(t.TempDir(), "credential-vault.age"), cat)
	if got := creds.CredentialSource("github"); got != "none" {
		t.Fatalf("initial source = %q", got)
	}
	t.Setenv("GITHUB_SEARCH_API_KEY", "environment-key")
	if got := creds.CredentialSource("github"); got != "environment" {
		t.Fatalf("environment source = %q", got)
	}
	testutil.FailErr(t, "store credential", creds.Set("github", "stored-key"))
	if got := creds.CredentialSource("github"); got != "stored" {
		t.Fatalf("stored source = %q", got)
	}
}

func TestDefaultSettingsExpandsResultsOnlyKeylessWithDirect(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: hn
    kind: keyless
    label: HN
    hint: hn
    test_query: t
    default_endpoint: https://hn.algolia.com
    roles: [results, seeds]
    default_enabled: true
  - id: arxiv
    kind: keyless
    label: arXiv
    hint: arxiv
    test_query: t
    default_endpoint: https://export.arxiv.org
    roles: [results]
    default_enabled: true
  - id: mwmbl
    kind: keyless
    label: Mwmbl
    hint: mwmbl
    test_query: t
    default_endpoint: https://mwmbl.org
  - id: brave
    kind: keyed
    label: Brave
    hint: brave
    test_query: t
    credential_slot: brave-search
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research-config.yaml"))
	s := DefaultSettings(nil, cfg, cat)
	assertStringSetEqual(t, "enabled", []string{"direct"}, s.EnabledProviders)
	assertStringSetEqual(t, "soft", []string{"arxiv"}, s.SoftProviderIDs)
}

func TestFilterKnownProviderIDsDropsDirectBundledKeyless(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: arxiv
    kind: keyless
    label: arXiv
    hint: arxiv
    test_query: t
    default_endpoint: https://export.arxiv.org
    default_enabled: true
  - id: mwmbl
    kind: keyless
    label: Mwmbl
    hint: mwmbl
    test_query: t
    default_endpoint: https://mwmbl.org
  - id: brave
    kind: keyed
    label: Brave
    hint: brave
    test_query: t
    credential_slot: brave-search
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	got := FilterKnownProviderIDs(cat, []string{"brave", "direct", "arxiv", "mwmbl", "unknown"})
	assertStringSetEqual(t, "filtered", []string{"brave", "direct", "mwmbl"}, got)
}

func TestDefaultSettingsStoredEmptyListWins(t *testing.T) {
	stageCatalogYAML(t, `
providers:
  - id: hn
    kind: keyless
    label: HN
    hint: hn
    test_query: t
    default_endpoint: https://hn.algolia.com
    default_enabled: true
`)
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	cfg := NewConfigStoreAt(filepath.Join(t.TempDir(), "web-research-config.yaml"))
	testutil.FailErr(t, "ApplyPrefs", cfg.ApplyPrefs(nil, nil, nil, []string{}))
	cfg = NewConfigStoreAt(cfg.path)
	s := DefaultSettings(nil, cfg, cat)
	if len(s.EnabledProviders) != 0 {
		t.Fatalf("enabled = %v want empty", s.EnabledProviders)
	}
}
