// Package destconfig attests outbound hosts from user-controlled device settings.
// Repository content and bundled catalogs cannot supply this approval fact.
package destconfig

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

// Source resolves user-configured hosts for a project or the whole device.
type Source struct {
	// Name identifies the configuration source on the approval card.
	Name        string
	Hosts       func(projectDir string) []string
	HostSources func(projectDir string) map[string]string
}

// Registry resolves a host against every wired source.
type Registry struct {
	mu      sync.RWMutex
	sources []Source
	cache   map[string]cacheEntry
	ttl     time.Duration
	now     func() time.Time
}

type cacheEntry struct {
	hosts map[string]string // host → source name
	at    time.Time
}

// NewRegistry caches per-project lookups briefly so settings changes become visible.
func NewRegistry() *Registry {
	return &Registry{cache: map[string]cacheEntry{}, ttl: 30 * time.Second, now: time.Now}
}

// Add wires a source. Sources with no lookup are ignored.
func (r *Registry) Add(s Source) {
	if r == nil || (s.Hosts == nil && s.HostSources == nil) {
		return
	}
	if s.Hosts != nil && strings.TrimSpace(s.Name) == "" && s.HostSources == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, s)
	r.cache = map[string]cacheEntry{}
}

// Configured reports whether the user's configuration names host for this
// project, and which source said so.
func (r *Registry) Configured(projectDir, host string) (string, bool) {
	if r == nil {
		return "", false
	}
	host = Normalize(host)
	if host == "" {
		return "", false
	}
	name, ok := r.hostsFor(projectDir)[host]
	return name, ok
}

func (r *Registry) hostsFor(projectDir string) map[string]string {
	r.mu.RLock()
	entry, ok := r.cache[projectDir]
	fresh := ok && r.now().Sub(entry.at) < r.ttl
	sources := r.sources
	r.mu.RUnlock()
	if fresh {
		return entry.hosts
	}

	hosts := map[string]string{}
	for _, s := range sources {
		if s.HostSources != nil {
			for raw, srcName := range s.HostSources(projectDir) {
				if h := Normalize(raw); h != "" {
					if _, taken := hosts[h]; !taken {
						hosts[h] = srcName
					}
				}
			}
		}
		if s.Hosts != nil {
			for _, raw := range s.Hosts(projectDir) {
				if h := Normalize(raw); h != "" {
					if _, taken := hosts[h]; !taken {
						hosts[h] = s.Name
					}
				}
			}
		}
	}
	r.mu.Lock()
	r.cache[projectDir] = cacheEntry{hosts: hosts, at: r.now()}
	r.mu.Unlock()
	return hosts
}

// Normalize reduces an endpoint to the hostname the egress broker observes. It
// accepts a bare host or a URL, the two shapes the user's configuration holds.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(u.Hostname()))
	}
	raw = strings.TrimSuffix(raw, ".")
	if raw == "" || strings.ContainsAny(raw, "/ \t") {
		return ""
	}
	return strings.ToLower(raw)
}
