package decide

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/config"
)

// Site names one host ranking seam the engine can rescore.
type Site string

// The reranking sites decisions.yaml declares. Every site is declared there;
// an absent declaration is a catalog fault, not a disabled site.
const (
	SiteSummarizeStructure   Site = "summarize_structure"
	SiteSummarizeDefinitions Site = "summarize_definitions"
	SiteSummarizeWindows     Site = "summarize_windows"
	SiteSummarizeNeighbors   Site = "summarize_neighbors"
	SiteSummarizeCallSites   Site = "summarize_call_sites"
	SiteSummarizeImports     Site = "summarize_imports"
	SiteSummarizeNextActions Site = "summarize_next_actions"
	SiteRepomapTags          Site = "repomap_tags"
	SiteProjectSearch        Site = "project_search"
	SiteWebPages             Site = "web_pages"
)

// sites lists every reranking site in catalog order.
var sites = []Site{
	SiteSummarizeStructure,
	SiteSummarizeDefinitions,
	SiteSummarizeWindows,
	SiteSummarizeNeighbors,
	SiteSummarizeCallSites,
	SiteSummarizeImports,
	SiteSummarizeNextActions,
	SiteRepomapTags,
	SiteProjectSearch,
	SiteWebPages,
}

// Sites returns every reranking site in catalog order.
func Sites() []Site {
	return slices.Clone(sites)
}

// KnownSite reports whether name is a declared site.
func KnownSite(name string) bool {
	for _, site := range sites {
		if string(site) == name {
			return true
		}
	}
	return false
}

// RerankPolicy is one site's engine budget and blend weight.
type RerankPolicy struct {
	// Enabled says whether the site asks the engine at all.
	Enabled bool
	// Deadline bounds the whole call; past it the lexical order stands.
	Deadline time.Duration
	// MaxCandidates is the lexical top K that reaches the engine.
	MaxCandidates int
	// Chunk is the number of candidates per engine request.
	Chunk int
	// Weight is added per unit of engine relevance on the site's score scale.
	Weight float64
}

// Policies maps every site to its policy.
type Policies map[Site]RerankPolicy

// For returns the site's policy; an undeclared site is disabled.
func (p Policies) For(site Site) RerankPolicy {
	if p == nil {
		return RerankPolicy{}
	}
	return p[site]
}

type policyRow struct {
	Enabled       bool    `yaml:"enabled"`
	DeadlineMS    int     `yaml:"deadline_ms"`
	MaxCandidates int     `yaml:"max_candidates"`
	Chunk         int     `yaml:"chunk"`
	Weight        float64 `yaml:"weight"`
}

// catalogVersion is the decisions.yaml format this package and
// internal/coordinator/turnload share.
const catalogVersion = 2

type catalogFile struct {
	Version int                  `yaml:"version"`
	Rerank  map[string]policyRow `yaml:"rerank"`
	// The turn-decision sections belong to internal/coordinator/turnload, which
	// parses the same file; they are named here so a strict decode accepts them.
	State     any `yaml:"state"`
	Turn      any `yaml:"turn"`
	Request   any `yaml:"request"`
	Lookup    any `yaml:"lookup"`
	ToolEvent any `yaml:"tool_event"`
}

// LoadPolicies reads and validates the rerank section of decisions.yaml.
func LoadPolicies() (Policies, error) {
	data, err := config.Read(config.Decisions)
	if err != nil {
		return nil, fmt.Errorf("read decisions: %w", err)
	}
	return ParsePolicies(data)
}

// ParsePolicies decodes and validates a decisions catalog.
func ParsePolicies(data []byte) (Policies, error) {
	var file catalogFile
	if err := config.DecodeYAML(data, &file); err != nil {
		return nil, fmt.Errorf("parse decisions: %w", err)
	}
	var faults []string
	if file.Version != catalogVersion {
		faults = append(faults, fmt.Sprintf("version %d is not %d", file.Version, catalogVersion))
	}
	out := make(Policies, len(sites))
	for name, row := range file.Rerank {
		if !KnownSite(name) {
			faults = append(faults, "rerank."+name+" is not a reranking site")
			continue
		}
		faults = append(faults, row.validate(name)...)
		out[Site(name)] = RerankPolicy{
			Enabled:       row.Enabled,
			Deadline:      time.Duration(row.DeadlineMS) * time.Millisecond,
			MaxCandidates: row.MaxCandidates,
			Chunk:         row.Chunk,
			Weight:        row.Weight,
		}
	}
	for _, site := range sites {
		if _, ok := file.Rerank[string(site)]; !ok {
			faults = append(faults, "rerank."+string(site)+" is not declared")
		}
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return nil, fmt.Errorf("decisions.yaml: %s", strings.Join(faults, "; "))
	}
	return out, nil
}

func (r policyRow) validate(name string) []string {
	var faults []string
	if r.DeadlineMS <= 0 {
		faults = append(faults, "rerank."+name+".deadline_ms must be positive")
	}
	if r.MaxCandidates <= 0 {
		faults = append(faults, "rerank."+name+".max_candidates must be positive")
	}
	if r.Chunk <= 0 || r.Chunk > r.MaxCandidates {
		faults = append(faults, "rerank."+name+".chunk must be in (0, max_candidates]")
	}
	if r.Weight < 0 {
		faults = append(faults, "rerank."+name+".weight must not be negative")
	}
	return faults
}
