package page

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CaptureToolName is the capture_page tool name.
const CaptureToolName = "capture_page"

// captureScope identifies the task tree used for secret matching.
func captureScope(tctx tools.ToolContext) captureprojection.Scope {
	return captureprojection.ScopeFor(tctx.ProjectID, tctx.ParentSessionID, tctx.SessionID)
}

type capturePageArgs struct {
	URL           string                  `json:"url"`
	ProjectDir    string                  `json:"project_dir"`
	Path          string                  `json:"path"`
	ProcessHandle string                  `json:"process_handle"`
	Actions       []browser.CaptureAction `json:"actions"`
	Selector      string                  `json:"selector"`
	Viewport      *captureViewport        `json:"viewport"`
	Wait          string                  `json:"wait"`
	Caption       string                  `json:"caption"`
	Capture       string                  `json:"capture"`
	Routes        []browser.RouteRule     `json:"routes"`
	Record        *browser.RecordOpts     `json:"record"`
}

type captureViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type capturePageFrame struct {
	Index          int                   `json:"index"`
	Caption        string                `json:"caption"`
	State          json.RawMessage       `json:"state"`
	Snapshot       json.RawMessage       `json:"snapshot"`
	EvidenceHandle string                `json:"evidence_handle,omitempty"`
	Coverage       *browser.MaskCoverage `json:"coverage,omitempty"`
}

type capturePageResult struct {
	State    json.RawMessage `json:"state"`
	Snapshot json.RawMessage `json:"snapshot"`
	browser.PageEvidence
	RoutesActive  int               `json:"routes_active,omitempty"`
	Mime          string            `json:"mime"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	Caption       string            `json:"caption,omitempty"`
	FinalURL      string            `json:"final_url,omitempty"`
	ActionResults []json.RawMessage `json:"action_results,omitempty"`
	Capture       string            `json:"capture,omitempty"`
	// Coverage tells the reader whether the frame's text was fully screened.
	Coverage *browser.MaskCoverage   `json:"coverage,omitempty"`
	Frames   []capturePageFrame      `json:"frames,omitempty"`
	Timeline *timelinearchive.Report `json:"timeline,omitempty"`
}

// CaptureHandler builds the capture_page handler for driving a user's web app.
func CaptureHandler(pool *browser.Pool, bg *bgprocess.Registry, live LivePreview) tools.ToolHandler {
	cap := &browser.CapturePool{Pool: pool}
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parseCapturePageArgs(args)
		if err != nil {
			return "", err
		}
		in.ProjectDir, err = resolveCaptureProjectDir(ctx, tctx, in.ProjectDir)
		if err != nil {
			return "", err
		}
		if err := requireCaptureProcess(bg, tctx.SessionID, in.URL, in.ProcessHandle); err != nil {
			return "", err
		}
		if err := requireLoopbackAuthority(in.URL, tctx); err != nil {
			return "", err
		}
		if err := resolveDriveFixtures(ctx, tctx, in.Routes, in.Actions); err != nil {
			return "", err
		}
		width, height := 0, 0
		if in.Viewport != nil {
			width = in.Viewport.Width
			height = in.Viewport.Height
		}
		req := browser.CaptureRequest{
			URL:          in.URL,
			ProjectDir:   in.ProjectDir,
			Path:         in.Path,
			Actions:      in.Actions,
			Selector:     in.Selector,
			Width:        width,
			Height:       height,
			Wait:         in.Wait,
			Caption:      in.Caption,
			Mode:         in.Capture,
			Routes:       in.Routes,
			CaptureScope: captureScope(tctx),
		}
		if in.Record != nil {
			req.Record = *in.Record
		}
		if live != nil {
			driveID := "drive:" + tctx.ToolCallID
			if strings.TrimSpace(tctx.ToolCallID) == "" {
				driveID = "drive:oneshot"
			}
			req.Preview = &browser.CapturePreview{
				Attach: func(held *browser.HeldPage) {
					live.Attach(ctx, preview.AttachOpts{
						ProjectID:          tctx.ProjectID,
						SessionID:          tctx.SessionID,
						ParentSessionID:    tctx.ParentSessionID,
						PageID:             driveID,
						AssistantMessageID: tctx.Invocation.MessageID,
						ToolCallID:         tctx.ToolCallID,
						Held:               held,
					})
				},
				Action: func(act browser.CaptureAction, result json.RawMessage) {
					live.PublishAction(ctx, tctx.SessionID, driveID, act, result)
				},
				Driving: func(driving bool) {
					live.PublishDriving(ctx, tctx.SessionID, driveID, driving)
				},
				Detach: func() {
					live.Detach(ctx, tctx.SessionID, driveID)
				},
			}
		}
		out, err := cap.Capture(ctx, req)
		if err != nil {
			return "", recordedFailure(tctx, out, err)
		}
		attachPageVisual(tctx, out)
		frames := make([]capturePageFrame, 0, len(out.Frames))
		for _, fr := range out.Frames {
			frames = append(frames, capturePageFrame{
				Index: fr.Index, Caption: fr.Caption,
				State: fr.State, Snapshot: fr.Snapshot, Coverage: fr.Coverage,
			})
		}
		payload, _ := surveyjson.Marshal(capturePageResult{
			State:         out.State,
			Snapshot:      out.Snapshot,
			PageEvidence:  out.PageEvidence,
			RoutesActive:  out.RoutesActive,
			Mime:          out.Mime,
			Width:         out.Width,
			Height:        out.Height,
			Caption:       out.Caption,
			FinalURL:      out.FinalURL,
			ActionResults: out.ActionLog,
			Capture:       captureMode(out.Mime),
			Coverage:      out.Coverage,
			Frames:        frames,
			Timeline:      out.Timeline,
		})
		return string(payload), nil
	}
}

func captureMode(mime string) string {
	switch mime {
	case browser.FilmstripMime:
		return browser.CaptureModeFilmstrip
	case timelinearchive.Mime:
		return browser.CaptureModeTimeline
	default:
		return browser.CaptureModeScreenshot
	}
}

func parseCapturePageArgs(args map[string]any) (capturePageArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return capturePageArgs{}, err
	}
	var in capturePageArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return capturePageArgs{}, err
	}
	in.URL = strings.TrimSpace(in.URL)
	in.ProjectDir = strings.TrimSpace(in.ProjectDir)
	in.Path = strings.TrimSpace(in.Path)
	in.ProcessHandle = strings.TrimSpace(in.ProcessHandle)
	in.Selector = strings.TrimSpace(in.Selector)
	in.Wait = strings.TrimSpace(in.Wait)
	in.Caption = strings.TrimSpace(in.Caption)
	in.Capture = strings.TrimSpace(in.Capture)
	mode, err := browser.NormalizeCaptureMode(in.Capture)
	if err != nil {
		return capturePageArgs{}, mapBrowserReject(err)
	}
	if in.Record != nil && mode != browser.CaptureModeTimeline {
		return capturePageArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_RECORD_INVALID", Data: map[string]any{"reason": "record_without_timeline", "capture": in.Capture}}
	}
	if in.Record != nil {
		if err := browser.ValidateRecordOpts(*in.Record); err != nil {
			return capturePageArgs{}, mapBrowserReject(err)
		}
	}
	if in.URL == "" && in.ProjectDir == "" {
		return capturePageArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "missing_target"}}
	}
	if in.URL != "" && in.ProjectDir != "" {
		return capturePageArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "url_and_project_dir"}}
	}
	if in.Path != "" && in.ProjectDir == "" {
		return capturePageArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "path_without_project_dir"}}
	}
	if in.ProcessHandle != "" && in.ProjectDir != "" {
		return capturePageArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "process_handle_with_project_dir"}}
	}
	if in.Path != "" {
		if _, err := browser.JoinStaticEntry(in.Path); err != nil {
			return capturePageArgs{}, mapBrowserReject(err)
		}
	}
	return in, nil
}

// requireCaptureProcess enforces host-tracked process handles for url-mode captures.
// url without process_handle remains allowed for user-started loopback servers.
func requireCaptureProcess(bg *bgprocess.Registry, sessionID, urlStr, handle string) error {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil
	}
	if strings.TrimSpace(urlStr) == "" {
		return &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "process_handle_without_url"}}
	}
	if bg == nil {
		return &toolrejection.ToolReject{Code: "CAPTURE_PROCESS_NOT_RUNNING", Data: map[string]any{"reason": "registry_unavailable", "handle": handle}}
	}
	if err := bg.RequireRunning(sessionID, handle); err != nil {
		code := "CAPTURE_PROCESS_NOT_RUNNING"
		reason := "not_running"
		if errors.Is(err, bgprocess.ErrProcessNotFound) {
			reason = "not_found"
		}
		return &toolrejection.ToolReject{Code: code, Data: map[string]any{"handle": handle, "reason": reason}}
	}
	return nil
}
