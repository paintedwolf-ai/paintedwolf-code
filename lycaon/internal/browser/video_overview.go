package browser

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/lycaon/lycaon/internal/contactsheet"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

// videoOverviewFrames is how many frames an overview spreads across a video.
const videoOverviewFrames = 12

// VideoSheet is a video's facts and a labeled sheet of the frames decoded from it.
type VideoSheet struct {
	Info   VideoInfo
	Frames []float64
	Sheet  []byte
}

// OverviewVideo spreads frames evenly across a whole video into one sheet.
func (p *Pool) OverviewVideo(ctx context.Context, media VideoMedia) (VideoSheet, error) {
	return p.DecodeVideoSheet(ctx, media, VideoFrameRequest{Spread: videoOverviewFrames, FrameWidth: DefaultVideoFrameWidth / 2})
}

// DecodeVideoSheet decodes the requested frames and lays them out with their timestamps.
func (p *Pool) DecodeVideoSheet(ctx context.Context, media VideoMedia, req VideoFrameRequest) (VideoSheet, error) {
	info, frames, err := p.DecodeVideo(ctx, media, req)
	if err != nil {
		return VideoSheet{}, err
	}
	if len(frames) == 0 {
		return VideoSheet{Info: info}, nil
	}
	cells := make([]contactsheet.Cell, 0, len(frames))
	at := make([]float64, 0, len(frames))
	for _, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f.JPEG))
		if err != nil {
			return VideoSheet{}, fmt.Errorf("decode video frame: %w", err)
		}
		cells = append(cells, contactsheet.Cell{Image: img, Label: contactsheet.ClockLabel(f.AtMS)})
		at = append(at, f.AtMS)
	}
	sheet, err := contactsheet.Compose(cells, contactsheet.Options{MaxEdge: providerwire.MaxImageDimension, Columns: sheetColumnsFor(len(cells), cells[0].Image)})
	if err != nil {
		return VideoSheet{}, err
	}
	return VideoSheet{Info: info, Frames: at, Sheet: sheet}, nil
}

// sheetColumnsFor lays tall frames out wider, so a portrait recording stays legible.
func sheetColumnsFor(n int, first image.Image) int {
	b := first.Bounds()
	cols := sheetColumns
	if b.Dy() > b.Dx() {
		cols = 6
	}
	return min(cols, n)
}
