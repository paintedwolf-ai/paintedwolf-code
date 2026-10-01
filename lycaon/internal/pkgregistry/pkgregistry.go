// Package pkgregistry loads the bundled catalog of public package registries:
// hosts anyone may read and only an authenticated publisher may write.
package pkgregistry

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// Registry is one public package registry.
type Registry struct {
	ID    string
	Title string
	// Hosts are exact normalized host names; no suffix matching.
	Hosts []string
	// Languages are filekind grammar names the registry serves.
	Languages []string
}

// Unregistered names supported languages with no public registry, and why.
type Unregistered struct {
	Languages []string
	Reason    string
}

// Catalog is the validated registry set.
type Catalog struct {
	registries   []Registry
	byID         map[string]Registry
	byHost       map[string]Registry
	unregistered []Unregistered
}

type catalogFile struct {
	Version      int                `yaml:"version"`
	Registries   []registryEntry    `yaml:"registries"`
	Unregistered []unregisteredFile `yaml:"unregistered"`
}

type registryEntry struct {
	ID        string   `yaml:"id"`
	Title     string   `yaml:"title"`
	Hosts     []string `yaml:"hosts"`
	Languages []string `yaml:"languages"`
}

type unregisteredFile struct {
	Languages []string `yaml:"languages"`
	Reason    string   `yaml:"reason"`
}

var (
	bundledOnce sync.Once
	bundled     *Catalog
	bundledErr  error
)

// Bundled returns the shipped catalog, loaded once.
func Bundled() (*Catalog, error) {
	bundledOnce.Do(func() {
		raw, err := config.Read(config.PackageRegistries)
		if err != nil {
			bundledErr = fmt.Errorf("package registry catalog: %w", err)
			return
		}
		bundled, bundledErr = Parse(raw)
	})
	return bundled, bundledErr
}

// Parse validates a catalog document.
func Parse(raw []byte) (*Catalog, error) {
	var parsed catalogFile
	if err := config.DecodeYAML(raw, &parsed); err != nil {
		return nil, fmt.Errorf("package registry catalog: %w", err)
	}
	if parsed.Version != 1 || len(parsed.Registries) == 0 {
		return nil, fmt.Errorf("package registry catalog: unsupported or empty version")
	}
	c := &Catalog{byID: map[string]Registry{}, byHost: map[string]Registry{}}
	languages := map[string]string{}
	claim := func(language, owner string) error {
		if prior, taken := languages[language]; taken {
			return fmt.Errorf("package registry catalog: language %q is listed by both %s and %s", language, prior, owner)
		}
		languages[language] = owner
		return nil
	}
	for _, entry := range parsed.Registries {
		r := Registry{
			ID:        strings.TrimSpace(entry.ID),
			Title:     strings.TrimSpace(entry.Title),
			Hosts:     normalizedHosts(entry.Hosts),
			Languages: trimmed(entry.Languages),
		}
		if r.ID == "" || r.Title == "" || len(r.Hosts) == 0 || len(r.Languages) == 0 {
			return nil, fmt.Errorf("package registry catalog: registry %q needs id, title, hosts, and languages", r.ID)
		}
		if _, duplicate := c.byID[r.ID]; duplicate {
			return nil, fmt.Errorf("package registry catalog: duplicate registry %q", r.ID)
		}
		for _, host := range r.Hosts {
			if prior, taken := c.byHost[host]; taken {
				return nil, fmt.Errorf("package registry catalog: host %q is listed by both %s and %s", host, prior.ID, r.ID)
			}
			c.byHost[host] = r
		}
		for _, language := range r.Languages {
			if err := claim(language, r.ID); err != nil {
				return nil, err
			}
		}
		c.byID[r.ID] = r
		c.registries = append(c.registries, r)
	}
	for i, entry := range parsed.Unregistered {
		u := Unregistered{Languages: trimmed(entry.Languages), Reason: strings.TrimSpace(entry.Reason)}
		if len(u.Languages) == 0 || u.Reason == "" {
			return nil, fmt.Errorf("package registry catalog: unregistered entry %d needs languages and a reason", i)
		}
		for _, language := range u.Languages {
			if err := claim(language, "unregistered"); err != nil {
				return nil, err
			}
		}
		c.unregistered = append(c.unregistered, u)
	}
	return c, nil
}

// ForHost returns the registry serving host. Matching is exact after normalization.
func (c *Catalog) ForHost(host string) (Registry, bool) {
	if c == nil {
		return Registry{}, false
	}
	r, ok := c.byHost[normalizeHost(host)]
	return r, ok
}

// ByID returns one registry.
func (c *Catalog) ByID(id string) (Registry, bool) {
	if c == nil {
		return Registry{}, false
	}
	r, ok := c.byID[strings.TrimSpace(id)]
	return r, ok
}

// Registries returns the registries in catalog order.
func (c *Catalog) Registries() []Registry {
	if c == nil {
		return nil
	}
	return slices.Clone(c.registries)
}

// Unregistered returns the languages declared to have no public registry.
func (c *Catalog) Unregistered() []Unregistered {
	if c == nil {
		return nil
	}
	return slices.Clone(c.unregistered)
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func normalizedHosts(hosts []string) []string {
	var out []string
	for _, host := range hosts {
		if h := normalizeHost(host); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func trimmed(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
