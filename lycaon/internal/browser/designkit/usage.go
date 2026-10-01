package designkit

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
)

// brandCSSPlaceholder marks the project stylesheet path in catalog text.
const brandCSSPlaceholder = "{brand_css}"

var (
	catalogUsageOnce  sync.Once
	catalogUsageValue CatalogUsage
	catalogUsageErr   error
)

// loadCatalogUsage caches validated catalog guidance.
func loadCatalogUsage() CatalogUsage {
	catalogUsageOnce.Do(func() {
		data, err := config.Read(config.DesignKitUsage)
		if err != nil {
			catalogUsageErr = fmt.Errorf("read design-kit-usage.yaml: %w", err)
			return
		}
		var doc struct {
			Fonts      string `yaml:"fonts"`
			IconNaming string `yaml:"icon_naming"`
			Themes     string `yaml:"themes"`
			Viewports  string `yaml:"viewports"`
			Tokens     string `yaml:"tokens"`
			Brand      string `yaml:"brand"`
			Assets     string `yaml:"assets"`
		}
		if err := config.DecodeYAML(data, &doc); err != nil {
			catalogUsageErr = fmt.Errorf("parse design-kit-usage.yaml: %w", err)
			return
		}
		catalogUsageValue = CatalogUsage{
			Fonts:     strings.TrimSpace(doc.Fonts),
			Icons:     strings.TrimSpace(doc.IconNaming),
			Themes:    strings.TrimSpace(doc.Themes),
			Viewports: strings.TrimSpace(doc.Viewports),
			Tokens:    strings.TrimSpace(doc.Tokens),
			Brand:     strings.ReplaceAll(strings.TrimSpace(doc.Brand), brandCSSPlaceholder, BrandCSSRel()),
			Assets:    strings.TrimSpace(doc.Assets),
		}
		for name, field := range map[string]string{
			"fonts": catalogUsageValue.Fonts, "icon_naming": catalogUsageValue.Icons,
			"themes": catalogUsageValue.Themes, "viewports": catalogUsageValue.Viewports,
			"tokens": catalogUsageValue.Tokens, "brand": catalogUsageValue.Brand,
			"assets": catalogUsageValue.Assets,
		} {
			if field == "" {
				catalogUsageErr = fmt.Errorf("design-kit-usage.yaml declares no %s", name)
				return
			}
		}
	})
	if catalogUsageErr != nil {
		panic(catalogUsageErr)
	}
	return catalogUsageValue
}

// IconNamingGuide is the naming recipe agents receive in the kit catalog.
func IconNamingGuide() string { return loadCatalogUsage().Icons }
