package visual

import (
	"testing"

	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFrameArchivesShareOneBoundAboveTheRasterCap(t *testing.T) {
	for _, mime := range []string{"application/vnd.lycaon.filmstrip+zip", timelinearchive.Mime} {
		if got := MaxBytesForMime(mime); got != MaxFrameArchiveBytes {
			t.Fatalf("MaxBytesForMime(%s) = %d, want %d", mime, got, MaxFrameArchiveBytes)
		}
	}
	if int64(MaxFrameArchiveBytes) <= MaxRasterBytes().Int64() {
		t.Fatalf("a frame archive bound (%d) must exceed the raster cap (%d)", MaxFrameArchiveBytes, MaxRasterBytes().Int64())
	}
}

func TestFrameArchiveKeepsItsDeclaredViewport(t *testing.T) {
	meta, _, err := prepareEntry(Entry{Meta: api.VisualArtifact{Mime: timelinearchive.Mime, Width: 800, Height: 600}, Bytes: []byte("PK")})
	if err != nil || meta.Width != 800 || meta.Height != 600 {
		t.Fatalf("timeline artifact = %+v, %v", meta, err)
	}
	// Any other opaque type is not sized from what its producer claims.
	meta, _, err = prepareEntry(Entry{Meta: api.VisualArtifact{Mime: "video/mp4", Width: 800, Height: 600}, Bytes: []byte("mp4")})
	if err != nil || meta.Width != 0 {
		t.Fatalf("video artifact = %+v, %v", meta, err)
	}
}
