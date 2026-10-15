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

// ReferenceCandidates is the size of the engine's reference batch, which
// REFERENCE_CANDIDATES in native/src/engine.rs defines.
const ReferenceCandidates = 16

// EngineRequirement is how fast a device must be for the engine to serve on it.
type EngineRequirement struct {
	// Reference bounds one reference batch of ReferenceCandidates candidates.
	Reference time.Duration
}

// Catalog is the part of decisions.yaml this package owns.
type Catalog struct {
	Policies Policies
	Engine   EngineRequirement
}

// catalogVersion is the decisions.yaml format this package and
// internal/coordinator/turnload share.
const catalogVersion = 2

type engineRow struct {
	ReferenceMS int `yaml:"reference_ms"`
}

type catalogFile struct {
	Version int                  `yaml:"version"`
	Engine  engineRow            `yaml:"engine"`
	Rerank  map[string]policyRow `yaml:"rerank"`
	// The turn-decision sections belong to internal/coordinator/turnload, which
	// parses the same file; they are named here so a strict decode accepts them.
	State     any `yaml:"state"`
	Turn      any `yaml:"turn"`
	Request   any `yaml:"request"`
	Lookup    any `yaml:"lookup"`
	ToolEvent any `yaml:"tool_event"`
}

// LoadCatalog reads and validates the engine and rerank sections of decisions.yaml.
func LoadCatalog() (Catalog, error) {
	data, err := config.Read(config.Decisions)
	if err != nil {
		return Catalog{}, fmt.Errorf("read decisions: %w", err)
	}
	return ParseCatalog(data)
}

// ParseCatalog decodes and validates a decisions catalog.
func ParseCatalog(data []byte) (Catalog, error) {
	var file catalogFile
	if err := config.DecodeYAML(data, &file); err != nil {
		return Catalog{}, fmt.Errorf("parse decisions: %w", err)
	}
	var faults []string
	if file.Version != catalogVersion {
		faults = append(faults, fmt.Sprintf("version %d is not %d", file.Version, catalogVersion))
	}
	engine := EngineRequirement{Reference: time.Duration(file.Engine.ReferenceMS) * time.Millisecond}
	if engine.Reference <= 0 {
		faults = append(faults, "engine.reference_ms must be positive")
	}
	out := make(Policies, len(sites))
	for name, row := range file.Rerank {
		if !KnownSite(name) {
			faults = append(faults, "rerank."+name+" is not a reranking site")
			continue
		}
		faults = append(faults, row.validate(name)...)
		policy := RerankPolicy{
			Enabled:       row.Enabled,
			Deadline:      time.Duration(row.DeadlineMS) * time.Millisecond,
			MaxCandidates: row.MaxCandidates,
			Chunk:         row.Chunk,
			Weight:        row.Weight,
		}
		faults = append(faults, engine.fits(name, policy)...)
		out[Site(name)] = policy
	}
	for _, site := range sites {
		if _, ok := file.Rerank[string(site)]; !ok {
			faults = append(faults, "rerank."+string(site)+" is not declared")
		}
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return Catalog{}, fmt.Errorf("decisions.yaml: %s", strings.Join(faults, "; "))
	}
	return Catalog{Policies: out, Engine: engine}, nil
}

// fits reports an enabled site the slowest qualifying device could not finish
// within its deadline.
func (e EngineRequirement) fits(name string, p RerankPolicy) []string {
	if !p.Enabled || e.Reference <= 0 {
		return nil
	}
	batches := (p.MaxCandidates + ReferenceCandidates - 1) / ReferenceCandidates
	if worst := time.Duration(batches) * e.Reference; worst > p.Deadline {
		return []string{fmt.Sprintf("rerank.%s.deadline_ms %d cannot fit %d reference batches of engine.reference_ms %d",
			name, p.Deadline.Milliseconds(), batches, e.Reference.Milliseconds())}
	}
	return nil
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
