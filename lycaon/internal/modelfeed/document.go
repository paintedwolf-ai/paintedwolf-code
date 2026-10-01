// Package modelfeed shares cached model metadata between the catalog and pricing.
package modelfeed

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
)

const DefaultURL = egressclass.ModelMetadataEndpoint

// CacheTTL bounds freshness for cached metadata.
const CacheTTL = 12 * time.Hour

// Status values for catalog_status wire projection.
const (
	StatusOK          = "ok"
	StatusStale       = "stale"
	StatusUnavailable = "unavailable"
)

type Document struct {
	Providers map[string]Provider `json:"-"`
	FetchedAt time.Time           `json:"-"`
	SourceURL string              `json:"-"`
}

type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Models map[string]Model `json:"models"`
}

type Model struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Family           string     `json:"family"`
	Modalities       Modalities `json:"modalities"`
	ToolCall         *bool      `json:"tool_call"`
	Temperature      bool       `json:"temperature"`
	StructuredOutput *bool      `json:"structured_output"`
	Reasoning        *bool      `json:"reasoning"`
	Attachment       bool       `json:"attachment"`
	Cost             *Cost      `json:"cost"`
	Limit            *Limit     `json:"limit"`
}

type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Cost uses USD per million tokens.
type Cost struct {
	ContextOver200K *Cost      `json:"context_over_200k"`
	Tiers           []CostTier `json:"tiers"`
	Input           *float64   `json:"input"`
	Output          *float64   `json:"output"`
	CacheRead       *float64   `json:"cache_read"`
	CacheWrite      *float64   `json:"cache_write"`
}

type CostTier struct {
	Cost
	Tier struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"tier"`
}

// Limit carries context/output token caps when present.
type Limit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// Meta is persisted beside the raw JSON cache.
type Meta struct {
	FetchedAt time.Time `json:"fetched_at"`
	SourceURL string    `json:"source_url"`
}

func ParseDocument(raw []byte) (*Document, error) {
	return parseDocument(raw, time.Time{}, DefaultURL)
}

func parseDocument(raw []byte, fetchedAt time.Time, sourceURL string) (*Document, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("modelfeed: decode root: %w", err)
	}
	providers := make(map[string]Provider, len(root))
	for key, blob := range root {
		var p Provider
		if err := json.Unmarshal(blob, &p); err != nil {
			return nil, fmt.Errorf("modelfeed: decode provider %q: %w", key, err)
		}
		if p.ID == "" {
			p.ID = key
		}
		if p.Models == nil {
			p.Models = map[string]Model{}
		}
		for mid, m := range p.Models {
			if m.ID == "" {
				m.ID = mid
				p.Models[mid] = m
			}
		}
		providers[key] = p
	}
	return &Document{
		Providers: providers,
		FetchedAt: fetchedAt,
		SourceURL: sourceURL,
	}, nil
}

// Provider returns the provider object for feed key, if present.
func (d *Document) Provider(feedKey string) (Provider, bool) {
	if d == nil {
		return Provider{}, false
	}
	p, ok := d.Providers[feedKey]
	return p, ok
}
