package rules

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// PathExcludeGroup is one ecosystem's dependency and build directories.
type PathExcludeGroup struct {
	ID        string   `yaml:"id"`
	Why       string   `yaml:"why,omitempty"`
	Languages []string `yaml:"languages"`
	Patterns  []string `yaml:"patterns"`
}

// NoVendorDirLanguage records a language without an excluded dependency directory.
type NoVendorDirLanguage struct {
	Language string `yaml:"language"`
	Why      string `yaml:"why"`
}

// NotExcludedPattern records an ambiguous directory that remains in scope.
type NotExcludedPattern struct {
	Pattern       string `yaml:"pattern"`
	DependencyFor string `yaml:"dependency_for"`
	SourceFor     string `yaml:"source_for"`
	HandledBy     string `yaml:"handled_by,omitempty"`
}

// PathExcludesConfig is the shared scan, summary, and snapshot path floor.
type PathExcludesConfig struct {
	Version     int                   `yaml:"version"`
	Groups      []PathExcludeGroup    `yaml:"groups"`
	NoVendorDir []NoVendorDirLanguage `yaml:"no_vendor_dir"`
	NotExcluded []NotExcludedPattern  `yaml:"not_excluded"`
}

// Patterns returns stable, deduplicated patterns in catalog order.
func (c *PathExcludesConfig) Patterns() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, 32)
	seen := make(map[string]struct{}, 32)
	for _, g := range c.Groups {
		for _, p := range g.Patterns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// GroupLeads returns one preview pattern per group.
func (c *PathExcludesConfig) GroupLeads() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Groups))
	for _, g := range c.Groups {
		for _, p := range g.Patterns {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// CoveredLanguages returns every language named by a group, deduped.
func (c *PathExcludesConfig) CoveredLanguages() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, 32)
	seen := make(map[string]struct{}, 32)
	for _, g := range c.Groups {
		for _, l := range g.Languages {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	return out
}

// LoadPathExcludes returns the bundled SAST path floor.
func LoadPathExcludes() (*PathExcludesConfig, error) {
	data, err := config.Read(config.ScanExcludes)
	if err != nil {
		return nil, err
	}
	var cfg PathExcludesConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse scan-excludes: %w", err)
	}
	if len(cfg.Groups) == 0 {
		return nil, fmt.Errorf("%s: no exclude groups", config.ScanExcludes)
	}
	return &cfg, nil
}
