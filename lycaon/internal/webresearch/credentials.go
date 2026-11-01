package webresearch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CredentialStore validates stored identifiers against its catalog.
type CredentialStore struct {
	store   *credentialstore.Store
	catalog *Catalog
}

const credentialContext = "web research"

// DefaultSlot describes where web-research credentials live on this platform.
func DefaultSlot() (credentialstore.Slot, error) {
	path, err := userCredentialsPath()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      path,
		Namespace: credentialstore.NamespaceWebResearch,
		Context:   credentialContext,
	}, nil
}

// NewCredentialStore loads credentials from the platform's store, accepting the
// slots cat declares.
func NewCredentialStore(cat *Catalog) (*CredentialStore, error) {
	slot, err := DefaultSlot()
	if err != nil {
		return nil, err
	}
	return openCredentialStore(slot, cat)
}

// NewCredentialStoreAt opens an empty store at path with a development identity file.
func NewCredentialStoreAt(path string, cat *Catalog) *CredentialStore {
	wrapper := &CredentialStore{catalog: cat}
	wrapper.store = credentialstore.NewEmpty(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceWebResearch, Context: credentialContext,
	}, wrapper.ValidID)
	return wrapper
}

func openCredentialStore(slot credentialstore.Slot, cat *Catalog) (*CredentialStore, error) {
	wrapper := &CredentialStore{catalog: cat}
	inner, err := credentialstore.Open(slot, wrapper.ValidID)
	if err != nil {
		return nil, err
	}
	wrapper.store = inner
	return wrapper, nil
}

func userCredentialsPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return credentialstore.DefaultVaultPath(dir), nil
}

// ValidID reports whether id is a credential slot this store's catalog declares.
func (s *CredentialStore) ValidID(id string) bool {
	if s == nil || s.catalog == nil {
		return false
	}
	for _, slot := range s.catalog.CredentialSlots() {
		if slot == id {
			return true
		}
	}
	return false
}

// Get returns the stored API key for id.
func (s *CredentialStore) Get(id string) (credentialstore.SecretValue, bool) {
	if s == nil || s.store == nil {
		return "", false
	}
	return s.store.Get(id)
}

// Set stores an API key for id.
func (s *CredentialStore) Set(id, apiKey string) error {
	if !s.ValidID(id) {
		return fmt.Errorf("unknown web research credential %q", id)
	}
	return s.store.Set(id, apiKey)
}

// Delete removes a stored key.
func (s *CredentialStore) Delete(id string) error {
	if !s.ValidID(id) {
		return fmt.Errorf("unknown web research credential %q", id)
	}
	return s.store.Delete(id)
}

// Configured reports whether a non-empty key is stored or available via a
// catalog-authorized environment variable.
func (s *CredentialStore) Configured(id string) bool {
	if key, ok := s.Get(id); ok && strings.TrimSpace(key.Value()) != "" {
		return true
	}
	entry, ok := s.catalog.byIDForSlot(id)
	if !ok || entry.APIKeyEnv == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv(entry.APIKeyEnv)) != ""
}

func (s *CredentialStore) CredentialSource(id string) string {
	if key, ok := s.Get(id); ok && strings.TrimSpace(key.Value()) != "" {
		return "stored"
	}
	entry, ok := s.catalog.byIDForSlot(id)
	if ok && entry.APIKeyEnv != "" && strings.TrimSpace(os.Getenv(entry.APIKeyEnv)) != "" {
		return "environment"
	}
	return "none"
}

func (c *Catalog) byIDForSlot(slot string) (CatalogEntry, bool) {
	if c == nil {
		return CatalogEntry{}, false
	}
	for _, entry := range c.Entries() {
		if entry.CredentialSlot == slot || entry.OptionalCredentialSlot == slot {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

const (
	searchResultCacheTTL = 12 * time.Minute
	searchResultCacheMax = 128
)

type cachedSearchResult struct {
	hits             []WebHit
	partial          bool
	providersSkipped []SkippedProvider
}

// searchResultCache reuses identical whole-search outcomes.
var searchResultCache = newTTLCache[cachedSearchResult](searchResultCacheTTL, searchResultCacheMax)

// normalizeSearchResultCacheKey keys the whole-search cache. Period is in the
// key: the same words under two windows are two different searches.
func normalizeSearchResultCacheKey(query string, period Period, limit int, settings Settings, explicitProvider string) string {
	return normalizeQueryKey(query) + "\x00" + period.String() + "\x00" +
		strconv.Itoa(limit) + "\x00" + searchSettingsFingerprint(settings, explicitProvider)
}

func searchSettingsFingerprint(settings Settings, explicitProvider string) string {
	var fingerprint strings.Builder
	fingerprint.WriteString("timeout:")
	fingerprint.WriteString(strconv.Itoa(settings.PerProviderTimeoutSec))
	fingerprint.WriteByte(';')
	if p := strings.TrimSpace(explicitProvider); p != "" {
		id := strings.ToLower(p)
		if id != directWireProviderID {
			fingerprint.WriteString("explicit:")
			appendProviderFingerprint(&fingerprint, id, settings)
			return hashSearchFingerprint(fingerprint.String())
		}
		fingerprint.WriteString("explicit:direct;")
	}
	providerIDs := effectiveProviderFingerprintIDs(settings)
	fingerprint.WriteString("providers:")
	for _, id := range providerIDs {
		appendProviderFingerprint(&fingerprint, id, settings)
	}
	return hashSearchFingerprint(fingerprint.String())
}

func effectiveProviderFingerprintIDs(settings Settings) []string {
	ids := append([]string(nil), settings.EnabledProviders...)
	ids = append(ids, settings.SoftProviderIDs...)
	// Direct can use every resolved seed-provider setting.
	for id := range settings.Keys {
		ids = append(ids, id)
	}
	for id := range settings.Config {
		ids = append(ids, id)
	}
	return normalizedProviderIDs(ids)
}

func normalizedProviderIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	return normalized
}

func appendProviderFingerprint(out *strings.Builder, id string, settings Settings) {
	out.WriteString(id)
	out.WriteByte('{')
	if key := strings.TrimSpace(settings.Keys[id]); key != "" {
		out.WriteString("auth=")
		out.WriteString(key)
		out.WriteByte(';')
	} else {
		out.WriteString("auth=absent;")
	}
	config := settings.Config[id]
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(config[key])
		out.WriteByte(';')
	}
	out.WriteByte('}')
}

func hashSearchFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func cachedWholeSearch(query string, period Period, limit int, settings Settings, explicitProvider string) (cachedSearchResult, bool) {
	return searchResultCache.get(normalizeSearchResultCacheKey(query, period, limit, settings, explicitProvider))
}

func storeWholeSearch(query string, period Period, limit int, settings Settings, explicitProvider string, result WebSearchResult) {
	if !result.OK || len(result.Results) == 0 {
		return
	}
	searchResultCache.put(normalizeSearchResultCacheKey(query, period, limit, settings, explicitProvider), cachedSearchResult{
		hits:             append([]WebHit(nil), result.Results...),
		partial:          result.Partial,
		providersSkipped: append([]SkippedProvider(nil), result.ProvidersSkipped...),
	})
}
