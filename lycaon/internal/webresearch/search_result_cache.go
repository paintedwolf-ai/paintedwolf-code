package webresearch

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
