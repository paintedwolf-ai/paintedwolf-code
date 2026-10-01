package webresearch

import (
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// DeclaredEndpointHosts is the endpoint host set one web_search fan-out reaches
// (results providers plus probed seed providers), derived from catalog and stored
// config rather than the model. The approval gate coalesces on it; it grants
// nothing, so a dial outside the set keeps its own per-host ask.
func DeclaredEndpointHosts(settings Settings, reg *Registry) []string {
	if reg == nil {
		return nil
	}
	resolved := resolvedSearchProviders(settings, reg)
	hosts := make([]string, 0, len(resolved))
	for _, provider := range resolved {
		if host := endpointHost(provider.endpoint); host != "" {
			hosts = append(hosts, host)
		}
	}
	return confine.NormalizeDeclaredHosts(hosts)
}

// resolvedProvider is one configured provider a fan-out would reach, paired with
// its resolved endpoint. The endpoint is empty when it is not operator-settable.
type resolvedProvider struct {
	id       string
	endpoint string
}

// resolvedSearchProviders walks the providers one web_search fan-out reaches:
// every results provider plus every catalog seeds-role provider the direct
// pipeline will probe.
func resolvedSearchProviders(settings Settings, reg *Registry) []resolvedProvider {
	if reg == nil {
		return nil
	}
	ids := make([]string, 0, len(settings.EnabledProviders)+len(settings.SoftProviderIDs))
	ids = append(ids, settings.EnabledProviders...)
	ids = append(ids, settings.SoftProviderIDs...)
	ids = append(ids, resolveSeedProviders(settings, reg)...)

	out := make([]resolvedProvider, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || isDirectProvider(id) {
			continue
		}
		if !reg.Has(id) {
			continue
		}
		if p := reg.Get(id); p == nil || !p.Configured(settings) {
			continue
		}
		out = append(out, resolvedProvider{id: id, endpoint: keylessEndpoint(settings, id)})
	}
	return out
}

// endpointHost extracts the dialed host from a provider endpoint. Endpoints are
// validated elsewhere; an unparseable one contributes nothing rather than a
// half-parsed string that could never match a real dial anyway.
func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
