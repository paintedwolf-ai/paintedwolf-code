package visual

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/pkg/api"
)

// IngestUserImage validates and stores one prompt image.
func IngestUserImage(ctx context.Context, store Store, rootSessionID, artifactID, mime string, raw []byte) (api.VisualArtifact, error) {
	if store == nil {
		return api.VisualArtifact{}, fmt.Errorf("visual store not configured")
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if err := RejectUnsafeUserImage(mime, raw); err != nil {
		return api.VisualArtifact{}, err
	}
	normalized, err := providerwire.NormalizeImageBytes(raw, mime, MaxRasterBytes())
	if err != nil {
		return api.VisualArtifact{}, err
	}
	return store.Put(ctx, rootSessionID, Entry{
		Meta: api.VisualArtifact{
			ID:       artifactID,
			Mime:     normalized.Mime,
			Source:   api.VisualArtifactSourceUser,
			Perceive: true,
		},
		Bytes: normalized.Bytes,
	})
}

// RejectUnsafeUserImage rejects active content (SVG/HTML/XML) and unknown mimes.
func RejectUnsafeUserImage(mime string, raw []byte) error {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "image/svg+xml", "text/html", "application/xhtml+xml", "text/xml", "application/xml":
		return fmt.Errorf("unsafe image mime %q rejected", mime)
	}
	if !format.IsRasterMIME(mime) {
		return fmt.Errorf("unsupported image mime %q", mime)
	}
	if len(raw) == 0 {
		return fmt.Errorf("image bytes are empty")
	}
	if format.LooksLikeActiveMarkup(raw) {
		return fmt.Errorf("image payload looks like active markup")
	}
	return nil
}
