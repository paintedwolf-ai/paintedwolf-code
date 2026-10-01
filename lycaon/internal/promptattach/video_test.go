package promptattach_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/pkg/api"
)

// An ISO base media header is enough for detection; the fake decoder decides what plays.
var mp4Header = append([]byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2avc1mp41"), make([]byte, 64)...)

type fakeVideoDecoder struct {
	undecodable bool
	decodes     int
}

func (f *fakeVideoDecoder) OverviewVideo(context.Context, string, []byte) (promptattach.VideoOverview, error) {
	f.decodes++
	if f.undecodable {
		return promptattach.VideoOverview{}, &promptattach.VideoUndecodableError{Reason: "media_error"}
	}
	return promptattach.VideoOverview{
		VideoFacts: promptattach.VideoFacts{DurationMS: 3000, Width: 320, Height: 180},
		FramesAtMS: []float64{125, 1500, 2875},
		Sheet:      []byte("\x89PNG sheet"),
	}, nil
}

func stagedDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "prompt-attachments"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read attachment dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

func TestVideoIsDecodedOnceAtUploadAndPromptsReadTheStoredOverview(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	caps := promptattach.Active()
	decoder := &fakeVideoDecoder{}
	receipt, err := promptattach.Upload(context.Background(), store, caps, decoder, "bug.mp4", "", bytes.NewReader(mp4Header))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if receipt.Kind != format.KindVideo || receipt.MIME != "video/mp4" || receipt.Video == nil || receipt.Video.Width != 320 || decoder.decodes != 1 {
		t.Fatalf("receipt = %+v (decodes %d)", receipt, decoder.decodes)
	}
	// The overview was kept at upload, so a prompt needs no decoder at all.
	for range 2 {
		res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
			[]api.PromptAttachmentPart{{BlobID: receipt.BlobID}})
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
		if len(res.Images) != 1 || res.Images[0].Mime != "image/png" || string(res.Images[0].Bytes) != "\x89PNG sheet" {
			t.Fatalf("images = %+v", res.Images)
		}
		if len(res.Parts) != 1 || res.Parts[0].BlobID != receipt.BlobID {
			t.Fatalf("parts = %+v", res.Parts)
		}
		fence := res.Parts[0].Fence
		for _, want := range []string{`duration_ms="3000"`, "0:00.1, 0:01.5, 0:02.9", "view_video(path=", res.Parts[0].Path} {
			if !strings.Contains(fence, want) {
				t.Fatalf("fence is missing %q:\n%s", want, fence)
			}
		}
	}
	if decoder.decodes != 1 {
		t.Fatalf("prompts decoded the video again: %d decodes", decoder.decodes)
	}
}

func TestVideoWhoseOverviewIsGoneIsDerivedAgainAtPromptTime(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	caps := promptattach.Active()
	decoder := &fakeVideoDecoder{}
	receipt, err := promptattach.Upload(context.Background(), store, caps, decoder, "bug.mp4", "", bytes.NewReader(mp4Header))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "prompt-attachments", receipt.BlobID, ".derived")); err != nil {
		t.Fatalf("remove derived overview: %v", err)
	}
	if _, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
		[]api.PromptAttachmentPart{{BlobID: receipt.BlobID}}); attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("without a decoder the missing overview should be unsupported, got %v", err)
	}
	res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), decoder,
		[]api.PromptAttachmentPart{{BlobID: receipt.BlobID}})
	if err != nil || len(res.Images) != 1 || decoder.decodes != 2 {
		t.Fatalf("re-derive: err=%v images=%d decodes=%d", err, len(res.Images), decoder.decodes)
	}
	// Derived again means kept again.
	if _, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
		[]api.PromptAttachmentPart{{BlobID: receipt.BlobID}}); err != nil {
		t.Fatalf("the re-derived overview was not kept: %v", err)
	}
}

func TestUndecodableVideoIsRefusedAtUploadAndLeavesNothingStaged(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	_, err := promptattach.Upload(context.Background(), store, promptattach.Active(), &fakeVideoDecoder{undecodable: true}, "bug.mov", "", bytes.NewReader(mp4Header))
	if attacherr.CodeOf(err) != attacherr.CodeUndecodable || !strings.Contains(err.Error(), "H.264") {
		t.Fatalf("err = %v", err)
	}
	if n := stagedDirs(t, root); n != 0 {
		t.Fatalf("an undecodable video left %d staged blobs", n)
	}
	// Without a browser there is no decoder, so a video cannot be accepted either.
	_, err = promptattach.Upload(context.Background(), store, promptattach.Active(), nil, "bug.mp4", "", bytes.NewReader(mp4Header))
	if attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("err without a decoder = %v", err)
	}
}

func TestVideoAboveTheVideoBoundIsRefused(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	caps := promptattach.Active()
	caps.Video.MaxBody = 16
	_, err := promptattach.Upload(context.Background(), store, caps, &fakeVideoDecoder{}, "bug.mp4", "", bytes.NewReader(mp4Header))
	if attacherr.CodeOf(err) != attacherr.CodeTooLarge {
		t.Fatalf("err = %v", err)
	}
}
