package webresearch

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/textrank"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// truncateText caps provider-supplied text at maxRunes, marking the cut. Runes,
// not bytes, so non-ASCII titles are never cut mid-rune.
func truncateText(s string, maxRunes int) string {
	return runeclamp.Clamp(strings.TrimSpace(s), maxRunes)
}

func hostDeniedReject(host string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{
		Code: "WEB_SEARCH_HOST_DENIED",
		Data: map[string]any{"host": strings.TrimSpace(host)},
	}
}

const (
	// SearchToolName is the native web_search tool id.
	SearchToolName = "web_search"
	// FetchURLToolName is the native fetch_url tool id.
	FetchURLToolName = "fetch_url"
)

func webResearchToolDisabled(cfg *ConfigStore) bool {
	return cfg != nil && !cfg.SearchEnabled()
}

// SearchToolRuntimeDeny returns a ProfilePolicyEngine runtime deny predicate.
func SearchToolRuntimeDeny(cfg *ConfigStore) func(toolName string) bool {
	return func(toolName string) bool {
		if !webResearchToolDisabled(cfg) {
			return false
		}
		return toolName == SearchToolName || toolName == FetchURLToolName
	}
}

// FilterSearchTool removes web research tools (web_search, fetch_url) from a tool name list.
func FilterSearchTool(names []string) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == SearchToolName || name == FetchURLToolName {
			continue
		}
		out = append(out, name)
	}
	return out
}

const shortenProviderQueryMaxTerms = 6

// shortenProviderQuery shortens a long natural-language search string into the
// analyzed primary terms vertical indexes (man pages, RFC corpora) rank well
// against. Short queries pass through unchanged. Year tokens are kept when
// present so versioned docs still match.
func shortenProviderQuery(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return q
	}
	// Already short enough that a vertical index can use it as-is.
	if len(strings.Fields(q)) <= shortenProviderQueryMaxTerms {
		return q
	}
	terms := textrank.AnalyzeQuery(q, false, true)
	if len(terms) == 0 {
		return q
	}
	out := make([]string, 0, shortenProviderQueryMaxTerms)
	for _, t := range terms {
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= shortenProviderQueryMaxTerms {
			break
		}
	}
	if len(out) == 0 {
		return q
	}
	return strings.Join(out, " ")
}

// looksLikeRFCName reports whether query names an RFC document id (rfc9110 /
// "RFC 9110"). Document `name` filters on datatracker expect this shape; NL
// title/abstract filters do not.
func looksLikeRFCName(query string) bool {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return false
	}
	q = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, q)
	if !strings.HasPrefix(q, "rfc") {
		return false
	}
	num := strings.TrimPrefix(q, "rfc")
	if num == "" {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

const (
	seedCallMaxParallelShared    = 2
	seedCallMaxParallelDedicated = 4
)

var (
	seedSemMu sync.Mutex
	seedSems  = map[string]chan struct{}{}
)

func seedCallParallelLimit(sharedWithCoordinator bool) int {
	if sharedWithCoordinator {
		return seedCallMaxParallelShared
	}
	return seedCallMaxParallelDedicated
}

func seedSemKey(providerID string, sharedWithCoordinator bool) string {
	if providerID == "" {
		providerID = "_unknown"
	}
	if sharedWithCoordinator {
		return providerID + ":shared"
	}
	return providerID + ":dedicated"
}

func seedCallSemFor(providerID string, sharedWithCoordinator bool) chan struct{} {
	key := seedSemKey(providerID, sharedWithCoordinator)
	seedSemMu.Lock()
	defer seedSemMu.Unlock()
	if sem, ok := seedSems[key]; ok {
		return sem
	}
	sem := make(chan struct{}, seedCallParallelLimit(sharedWithCoordinator))
	seedSems[key] = sem
	return sem
}

func acquireSeedCallSlot(ctx context.Context, providerID string, sharedWithCoordinator bool) error {
	sem := seedCallSemFor(providerID, sharedWithCoordinator)
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSeedCallSlot(providerID string, sharedWithCoordinator bool) {
	sem := seedCallSemFor(providerID, sharedWithCoordinator)
	select {
	case <-sem:
	default:
		wrlog.Warn("unbalanced seed-call slot release")
	}
}

func resetSeedCallSems() {
	seedSemMu.Lock()
	seedSems = map[string]chan struct{}{}
	seedSemMu.Unlock()
}

// rrfK is the standard Reciprocal Rank Fusion damping constant; higher
// values flatten the score gap between adjacent ranks.
const rrfK = 60

// fuseHits merges provider result lists with Reciprocal Rank Fusion:
// each URL scores sum(1/(rrfK+rank)) across the providers that returned
// it, so cross-provider agreement boosts a hit instead of being dropped
// as a duplicate. Equal scores keep provider registration order.
func fuseHits(outcomes []providerOutcome, maxResults int) []WebHit {
	type fusedHit struct {
		hit   WebHit
		score float64
		order int
	}
	byURL := make(map[string]*fusedHit)
	order := 0
	for _, outcome := range outcomes {
		if !outcome.ok {
			continue
		}
		for rank, hit := range outcome.hits {
			key := canonicalURL(hit.URL)
			entry, ok := byURL[key]
			if !ok {
				entry = &fusedHit{hit: hit, order: order}
				order++
				byURL[key] = entry
			}
			entry.score += 1 / float64(rrfK+rank+1)
		}
	}
	fused := make([]*fusedHit, 0, len(byURL))
	for _, entry := range byURL {
		fused = append(fused, entry)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].score != fused[j].score {
			return fused[i].score > fused[j].score
		}
		return fused[i].order < fused[j].order
	})
	if len(fused) > maxResults {
		fused = fused[:maxResults]
	}
	merged := make([]WebHit, 0, len(fused))
	for _, entry := range fused {
		merged = append(merged, entry.hit)
	}
	return merged
}

// canonicalURL is the dedup key shared by fusion, the frontier, seen-URL memory,
// and anchor harvesting: scheme and www collapse; default ports, trailing
// slashes, fragments, and tracking parameters drop. Unparseable input is used
// verbatim.
func canonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimSuffix(host, ":80")
	host = strings.TrimSuffix(host, ":443")
	query := u.Query()
	for param := range query {
		lower := strings.ToLower(param)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
			delete(query, param)
		}
	}
	key := "https://" + host + strings.TrimSuffix(u.Path, "/")
	if encoded := query.Encode(); encoded != "" {
		key += "?" + encoded
	}
	return key
}

// Provider family values for catalog-driven registration (runtime only).
const (
	FamilyPackageRegistry = "package_registry"
	FamilyDiscourse       = "discourse"
	FamilyStackExchange   = "stackexchange"
	FamilyMediawikiREST   = "mediawiki_rest"
	FamilyMediawikiAction = "mediawiki_action"
	FamilyCustom          = "custom"
)

// EffectiveFamily returns the registration family, defaulting omitted family to custom.
func (e CatalogEntry) EffectiveFamily() string {
	f := strings.TrimSpace(e.Family)
	if f == "" {
		return FamilyCustom
	}
	return f
}

func validateCatalogFamily(id string, entry CatalogEntry) error {
	family := entry.EffectiveFamily()
	switch family {
	case FamilyCustom, FamilyDiscourse, FamilyMediawikiREST, FamilyMediawikiAction:
		return nil
	case FamilyStackExchange:
		if strings.TrimSpace(entry.FamilyParams["site"]) == "" {
			return fmt.Errorf("web research catalog: %q family stackexchange requires family_params.site", id)
		}
		return nil
	case FamilyPackageRegistry:
		shape := strings.TrimSpace(entry.FamilyParams["shape"])
		if shape == "" {
			return fmt.Errorf("web research catalog: %q family package_registry requires family_params.shape", id)
		}
		if _, ok := packageRegistryShapes[shape]; !ok {
			return fmt.Errorf("web research catalog: %q has unknown package_registry shape %q", id, shape)
		}
		return nil
	default:
		return fmt.Errorf("web research catalog: %q has unknown family %q", id, family)
	}
}
