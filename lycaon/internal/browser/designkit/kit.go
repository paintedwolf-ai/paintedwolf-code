package designkit

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"strings"
)

// KitOrigin is the synthetic origin for hermetic kit assets (CDP Fetch only).
const KitOrigin = "http://lycaon.designkit/"

// ViewPath is the document path served for rasterize.
const ViewPath = "/view"

// BrandCSSRel() is the optional project overlay relative to the workspace root.
func BrandCSSRel() string { return settingsoverlay.Rel("design-kit.css") }

// MaxBrandCSSBytes caps project brand CSS injected into the shell.
const MaxBrandCSSBytes = 64 * 1024

// Options configure the host-injected mockup shell.
type Options struct {
	Theme    string // light|dark
	BrandCSS string // optional project overlay CSS (already read)
	BodyHTML string // authored body (html or svg markup)
}

// Catalog is the discoverable kit surface returned to agents.
type Catalog struct {
	Fonts      []string             `json:"fonts"`
	Icons      []string             `json:"icons"`
	IconGroups []IconGroup          `json:"icon_groups"`
	IconNaming string               `json:"icon_naming"`
	Themes     []string             `json:"themes"`
	Viewports  []string             `json:"viewports"`
	Origin     string               `json:"origin"`
	Assets     *AssetCatalogSummary `json:"assets,omitempty"`
	Usage      CatalogUsage         `json:"usage"`
}

// AssetCatalogSummary reports project files served into this render.
type AssetCatalogSummary struct {
	Count       int      `json:"count"`
	SamplePaths []string `json:"sample_paths"`
}

// CatalogUsage documents how agents consume kit affordances.
type CatalogUsage struct {
	Fonts     string `json:"fonts"`
	Icons     string `json:"icons"`
	Themes    string `json:"themes"`
	Viewports string `json:"viewports"`
	Tokens    string `json:"tokens"`
	Brand     string `json:"brand"`
	Assets    string `json:"assets"`
}

// GetCatalog returns the static discoverability payload.
func GetCatalog() Catalog {
	return Catalog{
		Fonts:      FontFamilies(),
		Icons:      IconNames(),
		IconGroups: IconGroups(),
		IconNaming: IconNamingGuide(),
		Themes:     Themes(),
		Viewports:  ViewportPresetNames(),
		Origin:     KitOrigin,
		Usage:      loadCatalogUsage(),
	}
}

// DocumentHTML builds the full offline HTML document for KitOrigin/view.
func DocumentHTML(opts Options) (string, error) {
	theme, err := NormalizeTheme(opts.Theme)
	if err != nil {
		return "", err
	}
	shellCSS, err := readEmbedded("shell.css")
	if err != nil {
		return "", fmt.Errorf("designkit shell: %w", err)
	}
	body := strings.TrimSpace(opts.BodyHTML)
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html data-kit-theme=\"")
	b.WriteString(theme)
	b.WriteString("\"><head><meta charset=\"utf-8\">")
	b.WriteString("<style>")
	b.WriteString(FontFaceCSS())
	b.Write(shellCSS)
	if css := strings.TrimSpace(opts.BrandCSS); css != "" {
		b.WriteString("\n/* project " + BrandCSSRel() + " */\n")
		b.WriteString(css)
	}
	b.WriteString("</style></head><body>")
	if refs := IconsReferencedInMarkup(body); len(refs) > 0 {
		b.WriteString(IconSpriteHTML(refs...))
	}
	b.WriteString(body)
	b.WriteString("</body></html>")
	return b.String(), nil
}

// LoadBrandCSS reads an optional project brand overlay; missing file is empty.
func LoadBrandCSS(projectRoot string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return "", nil
	}
	path := filepath.Join(projectRoot, filepath.FromSlash(BrandCSSRel()))
	// #nosec G304 -- path uses the fixed brand overlay location.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if len(data) > MaxBrandCSSBytes {
		return "", fmt.Errorf("brand css exceeds %d bytes", MaxBrandCSSBytes)
	}
	return string(data), nil
}
