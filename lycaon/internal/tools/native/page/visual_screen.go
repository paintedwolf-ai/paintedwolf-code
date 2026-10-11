package page

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visualscreen"
)

// screened is what one visual screen released for perception.
type screened struct {
	text       string
	bytes      []byte
	mime       string
	perception visualscreen.Perception
	gap        string
}

func (s screened) perceive() bool { return s.perception != visualscreen.PerceptionWithheld }

// resultPerception is empty for the original-perception default.
func (s screened) resultPerception() string {
	if s.perception == visualscreen.PerceptionOriginal {
		return ""
	}
	return string(s.perception)
}

// screenVisual runs the secret screen for tool, mapping bound and decode failures to
// the image rejections a path read uses.
func screenVisual(ctx context.Context, gate *visualscreen.Gate, tool string, tctx tools.ToolContext, in visualscreen.VisualScreenInput, rejectData map[string]any) (screened, error) {
	out := screened{bytes: in.RawBytes, mime: in.Mime, perception: visualscreen.PerceptionOriginal}
	if gate == nil {
		kind, err := visualscreen.Classify(in.Mime, in.RawBytes)
		if err == nil {
			switch kind {
			case visualscreen.KindRaster:
				if _, err := visualscreen.CheckRasterBounds(in.RawBytes); err != nil {
					return out, imageRejectFor(err, rejectData)
				}
			case visualscreen.KindSVG:
				if scanned, err := visualscreen.NewScanner(nil).Scan(ctx, in.Mime, in.RawBytes); err == nil {
					out.text = scanned.ExtractedText
				}
			}
		}
		return out, nil
	}
	in.SessionID, in.ProjectID = tctx.Identity.SessionID, tctx.Identity.ProjectID
	in.ToolName, in.ToolCallID = tool, tctx.Identity.ToolCallID
	outcome, err := gate.Screen(ctx, in)
	if err != nil {
		if errors.Is(err, visualscreen.ErrVisualSecretWithheld) {
			return out, &toolrejection.ToolReject{Code: "IMAGE_SECRET_WITHHELD", Data: rejectData}
		}
		return out, imageRejectFor(err, rejectData)
	}
	out.text = outcome.ExtractedText
	out.bytes = outcome.PerceiveBytes
	out.mime = outcome.Mime
	out.perception = outcome.Perception
	out.gap = string(outcome.Gap)
	return out, nil
}

func imageRejectFor(err error, data map[string]any) error {
	var dims *visualscreen.DimensionsError
	if errors.As(err, &dims) {
		return &toolrejection.ToolReject{Code: "IMAGE_DIMENSIONS_EXCEEDED", Data: withImageFacts(data, map[string]any{
			"width": dims.Width, "height": dims.Height, "max_dimension": dims.Max,
		})}
	}
	if errors.Is(err, visualscreen.ErrImageUndecodable) {
		return &toolrejection.ToolReject{Code: "IMAGE_CORRUPTED", Data: withImageFacts(data, map[string]any{
			"rejection_reason": err.Error(),
		})}
	}
	return err
}

func withImageFacts(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
