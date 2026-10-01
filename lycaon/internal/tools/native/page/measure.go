package page

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// MeasureToolName is the measure_page tool name.
const MeasureToolName = "measure_page"

type measurePageArgs struct {
	ID            string           `json:"id"`
	URL           string           `json:"url"`
	ProjectDir    string           `json:"project_dir"`
	Path          string           `json:"path"`
	ProcessHandle string           `json:"process_handle"`
	Selectors     []string         `json:"selectors"`
	Metrics       []string         `json:"metrics"`
	Viewport      *captureViewport `json:"viewport"`
	Wait          string           `json:"wait"`
	Annotate      bool             `json:"annotate"`
	Caption       string           `json:"caption"`
}

// MeasureHandler builds the measure_page handler for render-tree geometry probes.
func MeasureHandler(pool *browser.Pool, pages *pagesession.Registry, bg *bgprocess.Registry) tools.ToolHandler {
	mp := &browser.MeasurePool{Pool: pool}
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseMeasurePageArgs(args)
		if err != nil {
			return "", err
		}
		in.ProjectDir, err = resolveCaptureProjectDir(ctx, tctx, in.ProjectDir)
		if err != nil {
			return "", err
		}
		if err := requireLoopbackAuthority(in.URL, tctx); err != nil {
			return "", err
		}
		out, err := measurePage(ctx, mp, pages, bg, in, tctx)
		if err != nil {
			rej := &browserengine.RejectError{}
			if errors.As(err, &rej) {
				data := make(map[string]any, len(rej.Data)+2)
				for key, value := range rej.Data {
					data[key] = value
				}
				data["capture_held_target"], data["measure_page_id"] = in.ID != "", in.ID
				return "", &tools.ToolReject{Code: rej.Code, Data: data}
			}
			return "", err
		}
		if in.Annotate && len(out.Bytes) > 0 {
			if tctx.Out == nil {
				tctx.Out = &tools.ToolInvocationOut{}
			}
			tctx.Out.Visual = &tools.VisualCapture{
				Mime:      out.Mime,
				Bytes:     append([]byte(nil), out.Bytes...),
				Source:    api.VisualArtifactSourceCapture,
				Caption:   out.Caption,
				Perceive:  true,
				Projected: true,
			}
		}
		payload, err := browser.MarshalMeasureReport(out)
		if err != nil {
			return "", err
		}
		return string(payload), nil
	}
}

func measurePage(ctx context.Context, mp *browser.MeasurePool, pages *pagesession.Registry, bg *bgprocess.Registry, in measurePageArgs, tctx tools.ToolContext) (browser.MeasureResult, error) {
	req := browser.MeasureRequest{
		URL: in.URL, ProjectDir: in.ProjectDir, Path: in.Path, Selectors: in.Selectors,
		Metrics: in.Metrics, Wait: in.Wait, Annotate: in.Annotate, Caption: in.Caption,
		CaptureScope: captureScope(tctx),
	}
	if in.Viewport != nil {
		req.Width, req.Height = in.Viewport.Width, in.Viewport.Height
	}
	if in.ID != "" {
		entry, err := requirePage(pages, tctx.SessionID, in.ID)
		if err != nil {
			return browser.MeasureResult{}, err
		}
		return browser.MeasureHeld(ctx, entry.Held, in.ID, req)
	}
	if err := requireCaptureProcess(bg, tctx.SessionID, in.URL, in.ProcessHandle); err != nil {
		return browser.MeasureResult{}, err
	}
	return mp.Measure(ctx, req)
}

func parseMeasurePageArgs(args map[string]any) (measurePageArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return measurePageArgs{}, err
	}
	var in measurePageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return measurePageArgs{}, err
	}
	in.URL = strings.TrimSpace(in.URL)
	in.ID = strings.TrimSpace(in.ID)
	in.ProjectDir = strings.TrimSpace(in.ProjectDir)
	in.Path = strings.TrimSpace(in.Path)
	in.ProcessHandle = strings.TrimSpace(in.ProcessHandle)
	in.Wait = strings.TrimSpace(in.Wait)
	in.Caption = strings.TrimSpace(in.Caption)
	if in.ID != "" {
		if in.URL != "" || in.ProjectDir != "" || in.Path != "" || in.ProcessHandle != "" || in.Viewport != nil || in.Wait != "" {
			return measurePageArgs{}, &tools.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "id_with_navigation_target", "capture_held_target": true}}
		}
		if len(in.Selectors) == 0 {
			return measurePageArgs{}, &tools.ToolReject{Code: "MEASURE_SELECTORS_REQUIRED", Data: map[string]any{"reason": "empty_selectors"}}
		}
		return in, nil
	}
	if in.URL == "" && in.ProjectDir == "" {
		return measurePageArgs{}, &tools.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "missing_target"}}
	}
	if in.URL != "" && in.ProjectDir != "" {
		return measurePageArgs{}, &tools.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "url_and_project_dir"}}
	}
	if in.Path != "" && in.ProjectDir == "" {
		return measurePageArgs{}, &tools.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "path_without_project_dir"}}
	}
	if in.ProcessHandle != "" && in.ProjectDir != "" {
		return measurePageArgs{}, &tools.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "process_handle_with_project_dir"}}
	}
	if len(in.Selectors) == 0 {
		return measurePageArgs{}, &tools.ToolReject{Code: "MEASURE_SELECTORS_REQUIRED", Data: map[string]any{"reason": "empty_selectors"}}
	}
	if in.Path != "" {
		if _, err := browser.JoinStaticEntry(in.Path); err != nil {
			rej := &browserengine.RejectError{}
			if errors.As(err, &rej) {
				return measurePageArgs{}, &tools.ToolReject{Code: rej.Code, Data: rej.Data}
			}
			return measurePageArgs{}, err
		}
	}
	return in, nil
}
