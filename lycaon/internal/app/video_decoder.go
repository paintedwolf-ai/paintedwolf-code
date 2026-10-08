package app

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/promptattach"
)

// browserVideoDecoder reads attached videos with the managed browser's decoders.
type browserVideoDecoder struct{ pool *browser.Pool }

// videoDecoder is nil without a browser pool, so video attachments are refused.
func (b toolWiring) videoDecoder() promptattach.VideoDecoder {
	if b.browserPool == nil {
		return nil
	}
	return browserVideoDecoder{pool: b.browserPool}
}

func (d browserVideoDecoder) OverviewVideo(ctx context.Context, mime string, raw []byte) (promptattach.VideoOverview, error) {
	sheet, err := d.pool.OverviewVideo(ctx, browser.VideoMedia{MIME: mime, Bytes: raw})
	if err != nil {
		return promptattach.VideoOverview{}, videoDecodeError(err)
	}
	return promptattach.VideoOverview{
		VideoFacts: promptattach.VideoFacts{DurationMS: sheet.Info.DurationMS, Width: sheet.Info.Width, Height: sheet.Info.Height},
		FramesAtMS: sheet.Frames,
		Sheet:      sheet.Sheet,
	}, nil
}

func videoDecodeError(err error) error {
	var rej *browserengine.RejectError
	if errors.As(err, &rej) && rej.Code == "VIDEO_UNDECODABLE" {
		reason, _ := rej.Data["reason"].(string)
		return &promptattach.VideoUndecodableError{Reason: reason}
	}
	return err
}
