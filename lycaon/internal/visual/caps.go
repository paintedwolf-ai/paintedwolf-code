package visual

import (
	"strings"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/timelinearchive"
)

// MaxRasterBytes reads the boot-installed raster cap.
func MaxRasterBytes() bytebound.Transport {
	return promptattach.Active().Transport.MaxImage
}

// MaxFrameArchiveBytes bounds a filmstrip or timeline archive: its manifest and frames.
const MaxFrameArchiveBytes = 24 * 1024 * 1024

// MaxVideoBytes bounds one compressed live-tool recording.
const MaxVideoBytes = int(MaxDurableBodyBytes)

// MaxTreeMemoryBytes bounds hot bytes per session tree.
const MaxTreeMemoryBytes = 32 * 1024 * 1024

// IsRasterMime reports whether an artifact can occupy image-only surfaces.
func IsRasterMime(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/")
}

var interactivePreviewMIMEs = []string{"image/png", "image/jpeg", "image/webp", "image/gif", "image/svg+xml", "video/mp4"}

// InteractivePreviewMIMEs returns formats rendered by decision cards.
func InteractivePreviewMIMEs() []string { return append([]string(nil), interactivePreviewMIMEs...) }

// IsInteractivePreviewMime reports whether a decision card can render the artifact.
func IsInteractivePreviewMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	for _, candidate := range interactivePreviewMIMEs {
		if mime == candidate {
			return true
		}
	}
	return false
}

// IsFrameArchiveMime reports a filmstrip or timeline: a ZIP of frames and a manifest.
func IsFrameArchiveMime(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	return m == "application/vnd.lycaon.filmstrip+zip" || m == timelinearchive.Mime
}

// MaxBytesForMime returns the Put cap for an artifact mime type.
func MaxBytesForMime(mime string) int {
	if IsFrameArchiveMime(mime) {
		return MaxFrameArchiveBytes
	}
	if strings.EqualFold(strings.TrimSpace(mime), "video/mp4") {
		return MaxVideoBytes
	}
	return int(min(MaxRasterBytes().Int64(), MaxDurableBodyBytes))
}
