package browser

import (
	"fmt"

	"github.com/lycaon/lycaon/config"
)

// RenderBudgets caps project asset bytes served into one rasterize.
// SSOT: config/packs/painted-wolf/platform/host/render-budgets.yaml.
type RenderBudgets struct {
	// MaxAssetBytes bounds a single referenced file.
	MaxAssetBytes int
	// MaxRenderBytes bounds the sum of asset bytes served per rasterize.
	MaxRenderBytes int
	// MaxCatalogSamples bounds kit.assets sample_paths entries.
	MaxCatalogSamples int
}

type renderBudgetsFile struct {
	Version      int `yaml:"version"`
	RenderAssets struct {
		MaxAssetBytes     int `yaml:"max_asset_bytes"`
		MaxRenderBytes    int `yaml:"max_render_bytes"`
		MaxCatalogSamples int `yaml:"max_catalog_samples"`
	} `yaml:"render_assets"`
}

// LoadRenderBudgets reads the shipped render budgets.
func LoadRenderBudgets() (RenderBudgets, error) {
	raw, err := config.Read(config.RenderBudgets)
	if err != nil {
		return RenderBudgets{}, fmt.Errorf("read render budgets: %w", err)
	}
	var cfg renderBudgetsFile
	if err := config.DecodeYAML(raw, &cfg); err != nil {
		return RenderBudgets{}, fmt.Errorf("parse render budgets: %w", err)
	}
	if cfg.Version <= 0 {
		return RenderBudgets{}, fmt.Errorf("render budgets: version must be positive")
	}
	ra := cfg.RenderAssets
	if ra.MaxAssetBytes <= 0 || ra.MaxRenderBytes <= 0 || ra.MaxCatalogSamples <= 0 {
		return RenderBudgets{}, fmt.Errorf("render budgets: render_assets caps must be positive")
	}
	if ra.MaxAssetBytes > ra.MaxRenderBytes {
		return RenderBudgets{}, fmt.Errorf("render budgets: max_asset_bytes exceeds max_render_bytes")
	}
	return RenderBudgets{
		MaxAssetBytes:     ra.MaxAssetBytes,
		MaxRenderBytes:    ra.MaxRenderBytes,
		MaxCatalogSamples: ra.MaxCatalogSamples,
	}, nil
}
