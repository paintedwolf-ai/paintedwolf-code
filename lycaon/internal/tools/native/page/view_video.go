package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/contactsheet"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/visualscreen"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	ViewVideoToolName = "view_video"
	// defaultVideoFrames is how many frames a window spreads when no count is given.
	defaultVideoFrames = 12
)

// ViewVideoDeps provides runtime services for drawing frames from recordings.
type ViewVideoDeps struct {
	Boundary *sandbox.Boundary
	Pool     *browser.Pool
	Screen   *visualscreen.Gate
	// MaxBytes bounds one recording, which the decoder holds in memory.
	MaxBytes int64
}

type viewVideoArgs struct {
	Path    string             `json:"path"`
	TimesMS []float64          `json:"times_ms,omitempty"`
	StartMS *float64           `json:"start_ms,omitempty"`
	EndMS   *float64           `json:"end_ms,omitempty"`
	Count   int                `json:"count,omitempty"`
	Crop    *browser.VideoCrop `json:"crop,omitempty"`
}

type viewVideoFrame struct {
	AtMS float64 `json:"at_ms"`
	// Label is the timestamp printed on the frame's cell.
	Label string `json:"label"`
}

type viewVideoResult struct {
	Path       string             `json:"path"`
	Mime       string             `json:"mime"`
	DurationMS float64            `json:"duration_ms"`
	Width      int                `json:"width"`
	Height     int                `json:"height"`
	Frames     []viewVideoFrame   `json:"frames"`
	Crop       *browser.VideoCrop `json:"crop,omitempty"`
	Text       string             `json:"text,omitempty"`
	// Perception is omitted when the model sees the sheet as drawn.
	Perception   string `json:"perception,omitempty"`
	ScreeningGap string `json:"screening_gap,omitempty"`
}

// ViewVideoHandler builds the view_video handler: frames at chosen moments of a recording,
// drawn by the managed browser and laid out as one labeled sheet.
func ViewVideoHandler(deps ViewVideoDeps) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseViewVideoArgs(args)
		if err != nil {
			return "", err
		}
		resolved, err := projectpaths.ResolveRead(ctx, deps.Boundary, tctx, in.Path)
		if err != nil {
			return "", err
		}
		raw, err := readWorkspaceVideo(resolved, in.Path, deps.MaxBytes)
		if err != nil {
			return "", err
		}
		detected := format.Detect(resolved.DisplayPath, "", raw[:min(len(raw), format.PeekBytes)])
		if detected.Kind != format.KindVideo {
			return "", &tools.ToolReject{Code: "VIDEO_FORMAT_UNSUPPORTED", Data: map[string]any{"path": in.Path, "mime": detected.MIME}}
		}
		sheet, err := deps.Pool.DecodeVideoSheet(ctx, browser.VideoMedia{MIME: detected.MIME, Bytes: raw}, in.frameRequest())
		if err != nil {
			return "", videoReject(err, in.Path)
		}
		return renderVideoView(ctx, deps, tctx, in, resolved.DisplayPath, detected.MIME, sheet)
	}
}

// frameRequest spreads count frames across the window unless exact times were named. Fewer
// frames draw larger, so one frame keeps the detail a crop or a text check needs.
func (in viewVideoArgs) frameRequest() browser.VideoFrameRequest {
	req := browser.VideoFrameRequest{TimesMS: in.TimesMS, Crop: in.Crop}
	n := len(in.TimesMS)
	if n == 0 {
		n = in.Count
		if n == 0 {
			n = defaultVideoFrames
		}
		req.Spread = n
		if in.StartMS != nil {
			req.StartMS = *in.StartMS
		}
		if in.EndMS != nil {
			req.EndMS = *in.EndMS
		}
	}
	switch {
	case n == 1:
		req.FrameWidth = browser.MaxVideoFrameWidth
	case n <= 4:
		req.FrameWidth = browser.MaxVideoFrameWidth / 2
	default:
		req.FrameWidth = browser.MaxVideoFrameWidth / 4
	}
	return req
}

func renderVideoView(ctx context.Context, deps ViewVideoDeps, tctx tools.ToolContext, in viewVideoArgs, displayPath, mime string, sheet browser.VideoSheet) (string, error) {
	res := viewVideoResult{
		Path:       displayPath,
		Mime:       mime,
		DurationMS: sheet.Info.DurationMS,
		Width:      sheet.Info.Width,
		Height:     sheet.Info.Height,
		Frames:     make([]viewVideoFrame, len(sheet.Frames)),
		Crop:       in.Crop,
	}
	for i, at := range sheet.Frames {
		res.Frames[i] = viewVideoFrame{AtMS: at, Label: contactsheet.ClockLabel(at)}
	}
	if len(sheet.Sheet) > 0 {
		// Recordings are the user's screen, so frames pass the same secret screen as any image.
		view, err := screenVisual(ctx, deps.Screen, ViewVideoToolName, tctx, visualscreen.VisualScreenInput{
			Mime: "image/png", RawBytes: sheet.Sheet, SourcePath: displayPath,
		}, map[string]any{"path": in.Path, "format": "png"})
		if err != nil {
			return "", err
		}
		res.Text = runeclamp.Clamp(view.text, 4000)
		res.Perception = view.resultPerception()
		res.ScreeningGap = view.gap
		if view.perceive() {
			norm, err := providerwire.NormalizeImageBytes(view.bytes, view.mime, visual.MaxRasterBytes())
			if err != nil {
				return "", fmt.Errorf("normalize video sheet: %w", err)
			}
			if tctx.Out == nil {
				tctx.Out = &tools.ToolInvocationOut{}
			}
			tctx.Out.Visual = &tools.VisualCapture{
				Mime:      norm.Mime,
				Bytes:     norm.Bytes,
				Source:    api.VisualArtifactSourceWorkspace,
				Caption:   videoCaption(displayPath, res.Frames),
				Perceive:  true,
				Projected: true,
			}
		}
	}
	raw, _ := surveyjson.Marshal(res)
	return string(raw), nil
}

func videoCaption(path string, frames []viewVideoFrame) string {
	switch len(frames) {
	case 0:
		return path
	case 1:
		return fmt.Sprintf("%s at %s", path, frames[0].Label)
	}
	return fmt.Sprintf("%s, %s to %s", path, frames[0].Label, frames[len(frames)-1].Label)
}

func readWorkspaceVideo(resolved projectpaths.Resolved, modelPath string, maxBytes int64) ([]byte, error) {
	raw, err := resolved.ReadBounded(maxBytes)
	var tooLarge *projectpaths.FileTooLargeError
	switch {
	case err == nil:
		if len(raw) == 0 {
			return nil, &tools.ToolReject{Code: "VIDEO_FORMAT_UNSUPPORTED", Data: map[string]any{"path": modelPath}}
		}
		return raw, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, &tools.ToolReject{Code: "VIDEO_NOT_FOUND", Data: map[string]any{"path": modelPath}}
	case errors.Is(err, projectpaths.ErrIsDirectory):
		return nil, &tools.ToolReject{Code: "VIDEO_IS_DIRECTORY", Data: map[string]any{"path": modelPath}}
	case errors.As(err, &tooLarge):
		return nil, &tools.ToolReject{Code: "VIDEO_BYTES_EXCEEDED", Data: map[string]any{
			"path": modelPath, "bytes": tooLarge.Size, "max_bytes": maxBytes,
		}}
	default:
		return nil, fmt.Errorf("read %s: %w", modelPath, err)
	}
}

// videoReject carries the decoder's rejection with the path the model named.
func videoReject(err error, modelPath string) error {
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) {
		return err
	}
	data := map[string]any{"path": modelPath}
	for k, v := range rej.Data {
		data[k] = v
	}
	return &tools.ToolReject{Code: rej.Code, Data: data}
}

func parseViewVideoArgs(args map[string]any) (viewVideoArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return viewVideoArgs{}, err
	}
	var in viewVideoArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return viewVideoArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": "decode", "message": err.Error()})
	}
	in.Path = strings.TrimSpace(in.Path)
	invalid := func(reason, message string) (viewVideoArgs, error) {
		return viewVideoArgs{}, tools.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": reason, "message": message})
	}
	switch {
	case in.Path == "":
		return invalid("path_required", "path is required")
	case len(in.TimesMS) > 0 && (in.StartMS != nil || in.EndMS != nil || in.Count > 0):
		return invalid("times_and_window", "pass times_ms for exact moments, or start_ms, end_ms, and count for a window, not both")
	case in.StartMS != nil && in.EndMS != nil && *in.EndMS <= *in.StartMS:
		return invalid("end_before_start", "end_ms must be later than start_ms")
	}
	if n := max(len(in.TimesMS), in.Count); n > browser.MaxVideoFrames {
		return viewVideoArgs{}, &tools.ToolReject{Code: "VIDEO_FRAMES_BOUNDS", Data: map[string]any{"count": n, "max": browser.MaxVideoFrames}}
	}
	for _, t := range in.TimesMS {
		if t < 0 {
			return invalid("negative_time", "times_ms values must be zero or later")
		}
	}
	if (in.StartMS != nil && *in.StartMS < 0) || in.Count < 0 {
		return invalid("negative_window", "start_ms and count must not be negative")
	}
	if c := in.Crop; c != nil && (c.X < 0 || c.Y < 0 || c.Width <= 0 || c.Height <= 0) {
		return invalid("crop_invalid", "crop needs x and y of zero or more, and a positive width and height")
	}
	return in, nil
}
