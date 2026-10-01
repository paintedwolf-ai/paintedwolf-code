package promptattach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/contactsheet"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
)

// VideoFacts is what the decoder reports about a playable video.
type VideoFacts struct {
	DurationMS float64 `json:"duration_ms"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
}

// VideoOverview is a video's facts and one sheet of frames spread across it.
type VideoOverview struct {
	VideoFacts
	// FramesAtMS lists each sheet cell's time, in reading order.
	FramesAtMS []float64 `json:"frames_at_ms"`
	// Sheet is a PNG with each frame labeled by its timestamp.
	Sheet []byte `json:"-"`
}

// VideoDecoder reads videos with the managed browser's own decoders.
type VideoDecoder interface {
	OverviewVideo(ctx context.Context, mime string, raw []byte) (VideoOverview, error)
}

// VideoUndecodableError is a video the decoder cannot play.
type VideoUndecodableError struct{ Reason string }

func (e *VideoUndecodableError) Error() string { return "video undecodable: " + e.Reason }

// The overview is drawn once, at upload, and kept beside the blob under these names.
const (
	videoOverviewSheet = "overview.png"
	videoOverviewFacts = "overview.json"
	// maxVideoOverviewBytes bounds the stored sheet; a contact sheet is far smaller.
	maxVideoOverviewBytes = bytebound.Materialization(16 << 20)
)

// undecodableVideo names the codecs that play, since the user can re-export to one of them.
func undecodableVideo(name string) error {
	return attacherr.Undecodable(fmt.Sprintf("%q could not be decoded; attach an MP4 (H.264) or WebM (VP8, VP9, or AV1) recording", name))
}

// readVideo loads a stored video within the video byte bound.
func readVideo(store blobstore.Store, caps Caps, blob blobstore.Blob) ([]byte, error) {
	if blob.Size > caps.Video.MaxBody.Int64() {
		return nil, attacherr.TooLarge(fmt.Sprintf("%q is larger than the host decodes as a video", blob.Name))
	}
	return readBlob(store, blob, caps.Video.MaxBody.Int64())
}

// deriveVideoOverview decodes a stored video into its overview and keeps the overview beside
// the blob, so a prompt reads it back instead of decoding the video again.
func deriveVideoOverview(ctx context.Context, store blobstore.Store, caps Caps, video VideoDecoder, blob blobstore.Blob, mime string) (VideoOverview, error) {
	if video == nil {
		return VideoOverview{}, attacherr.Unsupported("video attachments need the managed browser, which is not available")
	}
	raw, err := readVideo(store, caps, blob)
	if err != nil {
		return VideoOverview{}, err
	}
	overview, err := video.OverviewVideo(ctx, mime, raw)
	var undecodable *VideoUndecodableError
	if errors.As(err, &undecodable) {
		return VideoOverview{}, undecodableVideo(blob.Name)
	}
	if err != nil {
		return VideoOverview{}, err
	}
	facts, err := json.Marshal(overview)
	if err != nil {
		return VideoOverview{}, err
	}
	if err := store.PutDerived(blob.ID, videoOverviewFacts, bytes.NewReader(facts), maxVideoOverviewBytes); err != nil {
		return VideoOverview{}, fmt.Errorf("keep video facts: %w", err)
	}
	if err := store.PutDerived(blob.ID, videoOverviewSheet, bytes.NewReader(overview.Sheet), maxVideoOverviewBytes); err != nil {
		return VideoOverview{}, fmt.Errorf("keep video overview: %w", err)
	}
	return overview, nil
}

// storedVideoOverview reads the overview kept at upload; blobstore.ErrNotFound when none was.
func storedVideoOverview(store blobstore.Store, blob blobstore.Blob) (VideoOverview, error) {
	facts, err := readDerived(store, blob.ID, videoOverviewFacts)
	if err != nil {
		return VideoOverview{}, err
	}
	var overview VideoOverview
	if err := json.Unmarshal(facts, &overview); err != nil {
		return VideoOverview{}, fmt.Errorf("decode stored video facts: %w", err)
	}
	if overview.Sheet, err = readDerived(store, blob.ID, videoOverviewSheet); err != nil {
		return VideoOverview{}, err
	}
	return overview, nil
}

func readDerived(store blobstore.Store, id, name string) ([]byte, error) {
	f, err := store.OpenDerived(id, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxVideoOverviewBytes.Int64()))
}

// projectVideo sends the stored overview sheet as an image and frames the video's facts
// with a pointer to view_video for any other moment. A blob whose overview is missing
// is derived again, since the decoder is the only way to make one.
func projectVideo(ctx context.Context, p projection, out *IngestResult) error {
	overview, err := storedVideoOverview(p.store, p.blob)
	if errors.Is(err, blobstore.ErrNotFound) {
		overview, err = deriveVideoOverview(ctx, p.store, p.caps, p.video, p.blob, p.detected.MIME)
	}
	if err != nil {
		return err
	}
	out.Images = append(out.Images, InlineImage{Mime: "image/png", Bytes: overview.Sheet})
	fence := FormatFence(p.blob.Name, p.detected.MIME, videoOverviewBody(overview), false,
		fmt.Sprintf("bytes=%q", strconv.FormatInt(p.blob.Size, 10)),
		fmt.Sprintf("duration_ms=%q", strconv.FormatFloat(overview.DurationMS, 'f', -1, 64)),
		fmt.Sprintf("width=%q", strconv.Itoa(overview.Width)),
		fmt.Sprintf("height=%q", strconv.Itoa(overview.Height)),
		fmt.Sprintf("path=%q", p.blob.Rel),
	) + "\n" + VideoHint(p.blob.Rel)
	out.Parts = append(out.Parts, framedPayload(p.blob, p.detected.MIME, fence))
	return nil
}

// VideoHint names the tool that draws any other moment of an attached video.
func VideoHint(path string) string {
	return fmt.Sprintf("[full video: view_video(path=%q) with times_ms, or start_ms and end_ms, to see other moments]", path)
}

func videoOverviewBody(o VideoOverview) string {
	at := make([]string, len(o.FramesAtMS))
	for i, ms := range o.FramesAtMS {
		at[i] = contactsheet.ClockLabel(ms)
	}
	return fmt.Sprintf("Video, %s long at %dx%d. The attached sheet shows %d frames in reading order, at %s.",
		contactsheet.ClockLabel(o.DurationMS), o.Width, o.Height, len(at), strings.Join(at, ", "))
}
