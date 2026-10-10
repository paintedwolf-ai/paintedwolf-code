package page

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCaptureMeasurementAndVisualTargetsRefuseInvalidCoordinates(t *testing.T) {
	pages := pagesession.NewRegistry(pagesession.Config{MaxPages: 1})
	t.Cleanup(func() { pages.Close(t.Context()) })
	capture := CaptureHandler(nil, nil, nil)
	measure := MeasureHandler(nil, pages, nil)
	image := ViewImageHandler(ViewImageDeps{})
	video := ViewVideoHandler(ViewVideoDeps{})
	render := RenderViewHandler(nil, nil, nil)
	cases := []struct {
		name    string
		handler tools.ToolHandler
		args    map[string]any
		code    string
	}{
		{"capture conflicting roots", capture, map[string]any{"url": "https://example.com", "project_dir": "."}, "CAPTURE_TARGET_INVALID"},
		{"capture path without root", capture, map[string]any{"url": "https://example.com", "path": "index.html"}, "CAPTURE_TARGET_INVALID"},
		{"capture static process", capture, map[string]any{"project_dir": ".", "process_handle": "process"}, "CAPTURE_TARGET_INVALID"},
		{"capture escaping path", capture, map[string]any{"project_dir": ".", "path": "../outside"}, "CAPTURE_TARGET_INVALID"},
		{"capture invalid mode", capture, map[string]any{"url": "https://example.com", "capture": "invalid"}, "CAPTURE_MODE_INVALID"},
		{"capture recording without timeline", capture, map[string]any{"url": "https://example.com", "record": map[string]any{}}, "CAPTURE_RECORD_INVALID"},
		{"capture invalid recording", capture, map[string]any{"url": "https://example.com", "capture": "timeline", "record": map[string]any{"tail_ms": -1}}, "CAPTURE_RECORD_INVALID"},
		{"capture static root unavailable", capture, map[string]any{"project_dir": "."}, "CAPTURE_PROJECT_DIR_MISSING"},
		{"capture process unavailable", capture, map[string]any{"url": "https://example.com", "process_handle": "missing"}, "CAPTURE_PROCESS_NOT_RUNNING"},
		{"measure conflicting roots", measure, map[string]any{"url": "https://example.com", "project_dir": "."}, "CAPTURE_TARGET_INVALID"},
		{"measure path without root", measure, map[string]any{"url": "https://example.com", "path": "index.html"}, "CAPTURE_TARGET_INVALID"},
		{"measure static process", measure, map[string]any{"project_dir": ".", "process_handle": "process"}, "CAPTURE_TARGET_INVALID"},
		{"measure selectors required", measure, map[string]any{"url": "https://example.com"}, "MEASURE_SELECTORS_REQUIRED"},
		{"measure escaping path", measure, map[string]any{"project_dir": ".", "selectors": []string{"body"}, "path": "../outside"}, "CAPTURE_TARGET_INVALID"},
		{"render missing source", render, map[string]any{}, "TOOL_ARGS_INVALID"},
		{"render unsupported destination", render, map[string]any{"markup": "<p>ok</p>", "dest": "result.jpg"}, "TOOL_ARGS_INVALID"},
		{"image missing source", image, map[string]any{}, "TOOL_ARGS_INVALID"},
		{"image conflicting source", image, map[string]any{"path": "image.png", "handle": "render"}, "TOOL_ARGS_INVALID"},
		{"image negative scale", image, map[string]any{"path": "image.png", "scale": -1}, "TOOL_ARGS_INVALID"},
		{"image missing handle", image, map[string]any{"handle": "missing"}, "RENDER_HANDLE_NOT_FOUND"},
		{"video missing source", video, map[string]any{}, "TOOL_ARGS_INVALID"},
		{"video mixed times and window", video, map[string]any{"path": "video.mp4", "times_ms": []float64{1}, "count": 1}, "TOOL_ARGS_INVALID"},
		{"video reversed window", video, map[string]any{"path": "video.mp4", "start_ms": 2, "end_ms": 1}, "TOOL_ARGS_INVALID"},
		{"video negative time", video, map[string]any{"path": "video.mp4", "times_ms": []float64{-1}}, "TOOL_ARGS_INVALID"},
		{"video negative count", video, map[string]any{"path": "video.mp4", "count": -1}, "TOOL_ARGS_INVALID"},
		{"video invalid crop", video, map[string]any{"path": "video.mp4", "crop": map[string]any{"width": 0, "height": 10}}, "TOOL_ARGS_INVALID"},
		{"video excessive frames", video, map[string]any{"path": "video.mp4", "count": 10000}, "VIDEO_FRAMES_BOUNDS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.handler(t.Context(), tc.args, tools.ToolContext{})
			var reject *toolrejection.ToolReject
			if out != "" || !errors.As(err, &reject) || reject.Code != tc.code {
				t.Fatalf("out=%q err=%v want=%s", out, err, tc.code)
			}
		})
	}
}
