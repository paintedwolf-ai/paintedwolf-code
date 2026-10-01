package browser

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/browserengine"
)

var (
	forbiddenMarkupPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)<script\b`),
		regexp.MustCompile(`(?i)<iframe\b`),
		regexp.MustCompile(`(?i)<object\b`),
		regexp.MustCompile(`(?i)<embed\b`),
		regexp.MustCompile(`(?i)<link\b`),
		regexp.MustCompile(`(?i)<meta\b[^>]+http-equiv`),
		regexp.MustCompile(`(?i)\bon[a-z0-9_-]+\s*=`),
		regexp.MustCompile(`(?i)javascript\s*:`),
		regexp.MustCompile(`(?i)@import\b`),
		regexp.MustCompile(`(?i)url\s*\(\s*['"]?\s*https?://`),
		regexp.MustCompile(`(?i)xlink:href\s*=\s*['"]?\s*https?://`),
		regexp.MustCompile(`(?i)\bhref\s*=\s*['"]?\s*https?://`),
		regexp.MustCompile(`(?i)\bsrc\s*=\s*['"]?\s*https?://`),
	}

	// AssetOrigin references resolve through the offline fetch handler.
	assetOriginPattern = regexp.QuoteMeta(AssetOrigin)

	assetStylesheetLinkRe = regexp.MustCompile(`(?i)<link\s+(?:(?:rel\s*=\s*["']?stylesheet["']?|href\s*=\s*["']?` + assetOriginPattern + `[^"'\s>]*["']?|type\s*=\s*["']?text/css["']?|media\s*=\s*["'][^"']*["'])\s*)+/?>`)
	assetImportRe         = regexp.MustCompile(`(?i)@import\s+(?:url\s*\(\s*)?["']?` + assetOriginPattern + `[^"')\s;]*["']?\s*\)?\s*;?`)
	assetOriginRe         = regexp.MustCompile(`(?i)` + assetOriginPattern)

	// svgForbiddenExtra applies only to served SVG assets.
	svgForbiddenExtra = regexp.MustCompile(`(?i)<foreignobject\b`)

	// cssForbiddenPatterns applies after asset-origin masking.
	cssForbiddenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)@import\b`),
		regexp.MustCompile(`(?i)url\s*\(\s*['"]?\s*https?://`),
		regexp.MustCompile(`(?i)javascript\s*:`),
	}
)

// maskHermeticAssetRefs removes references handled by the offline fetcher.
func maskHermeticAssetRefs(markup string) string {
	masked := assetStylesheetLinkRe.ReplaceAllString(markup, " ")
	masked = assetImportRe.ReplaceAllString(masked, " ")
	return assetOriginRe.ReplaceAllString(masked, "asset-ref:")
}

// scanForbiddenMarkup returns the first forbidden unhandled reference.
func scanForbiddenMarkup(lowerMarkup string) string {
	masked := maskHermeticAssetRefs(lowerMarkup)
	for _, re := range forbiddenMarkupPatterns {
		if re.MatchString(masked) {
			return re.String()
		}
	}
	return ""
}

// sanitizeSVGAsset rejects active or external SVG content.
func sanitizeSVGAsset(body []byte) *browserengine.RejectError {
	lower := strings.ToLower(string(body))
	pattern := scanForbiddenMarkup(lower)
	if pattern == "" && svgForbiddenExtra.MatchString(lower) {
		pattern = svgForbiddenExtra.String()
	}
	if pattern != "" {
		return &browserengine.RejectError{Code: "RENDER_ASSET_DENIED", Data: map[string]any{
			"reason":  "svg_active_content",
			"pattern": pattern,
		}}
	}
	return nil
}

// sanitizeCSSAsset rejects CSS references outside the asset origin.
func sanitizeCSSAsset(body []byte) *browserengine.RejectError {
	masked := maskHermeticAssetRefs(strings.ToLower(string(body)))
	for _, re := range cssForbiddenPatterns {
		if re.MatchString(masked) {
			return &browserengine.RejectError{Code: "RENDER_ASSET_DENIED", Data: map[string]any{
				"reason":  "css_external_ref",
				"pattern": re.String(),
			}}
		}
	}
	return nil
}

// ValidateMarkup enforces offline, no-script markup bounds before rasterize.
func ValidateMarkup(markup, mime string) error {
	markup = strings.TrimSpace(markup)
	if markup == "" {
		return browserengine.Reject("RENDER_MARKUP_INVALID", map[string]any{"reason": "empty_markup"})
	}
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "svg", "html":
	default:
		return browserengine.Reject("RENDER_MARKUP_INVALID", map[string]any{"reason": "unsupported_mime", "mime": mime})
	}
	if len(markup) > MaxMarkupBytes {
		return browserengine.Reject("RENDER_MARKUP_OVERSIZED", map[string]any{
			"bytes":     len(markup),
			"max_bytes": MaxMarkupBytes,
		})
	}
	if pattern := scanForbiddenMarkup(strings.ToLower(markup)); pattern != "" {
		return browserengine.Reject("RENDER_MARKUP_FORBIDDEN", map[string]any{
			"pattern": pattern,
		})
	}
	return nil
}
