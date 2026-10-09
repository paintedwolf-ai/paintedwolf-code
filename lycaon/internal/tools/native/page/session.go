package page

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// LivePreview is the optional read-only screencast sink for held pages.
type LivePreview interface {
	Attach(ctx context.Context, opts preview.AttachOpts)
	Claim(ctx context.Context, sessionID, pageID, assistantMessageID, toolCallID string)
	Detach(ctx context.Context, sessionID, pageID string)
	PublishAction(ctx context.Context, sessionID, pageID string, act browser.CaptureAction, result json.RawMessage)
	PublishDriving(ctx context.Context, sessionID, pageID string, driving bool)
}

// Tool names for the page session family.
const (
	OpenToolName     = "page_open"
	ActToolName      = "page_act"
	SnapshotToolName = "page_snapshot"
	CloseToolName    = "page_close"
)

type pageOpenArgs struct {
	URL           string              `json:"url"`
	ProjectDir    string              `json:"project_dir"`
	Path          string              `json:"path"`
	ProcessHandle string              `json:"process_handle"`
	Viewport      *captureViewport    `json:"viewport"`
	Wait          string              `json:"wait"`
	Routes        []browser.RouteRule `json:"routes"`
}

type pageActArgs struct {
	ID      string                  `json:"id"`
	Actions []browser.CaptureAction `json:"actions"`
	Record  *browser.RecordOpts     `json:"record"`
}

type pageSnapshotArgs struct {
	ID       string `json:"id"`
	Selector string `json:"selector"`
	Caption  string `json:"caption"`
}

type pageCloseArgs struct {
	ID       string `json:"id"`
	Snapshot bool   `json:"snapshot"`
	Selector string `json:"selector"`
	Caption  string `json:"caption"`
}

// OpenResult is the page_open tool payload.
type OpenResult struct {
	ID           string   `json:"id"`
	FinalURL     string   `json:"final_url,omitempty"`
	RoutesActive int      `json:"routes_active,omitempty"`
	LivePages    []string `json:"live_pages"`
	MaxPages     int      `json:"max_pages"`
}

// pageActResult reports each action and what the page did while they ran.
type pageActResult struct {
	ID            string            `json:"id"`
	ActionResults []json.RawMessage `json:"action_results,omitempty"`
	browser.PageEvidence
	RoutesActive int                     `json:"routes_active,omitempty"`
	Timeline     *timelinearchive.Report `json:"timeline,omitempty"`
	// Coverage tells the reader whether the recording's text was fully screened.
	Coverage  *browser.MaskCoverage `json:"coverage,omitempty"`
	LivePages []string              `json:"live_pages"`
}

type pageUniversalResult struct {
	ID       string          `json:"id"`
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
	LivePages     []string          `json:"live_pages"`
	Closed        bool              `json:"closed,omitempty"`
}

// OpenHandler builds the page_open handler. Opening the page already held for a target
// reloads it with the given viewport and routes.
func OpenHandler(pool *browser.Pool, pages *pagesession.Registry, bg *bgprocess.Registry, live LivePreview) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parsePageOpenArgs(args)
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
		if err := resolveDriveFixtures(ctx, tctx, in.Routes, nil); err != nil {
			return "", err
		}
		width, height := 0, 0
		if in.Viewport != nil {
			width, height = in.Viewport.Width, in.Viewport.Height
		}
		entry, err := reopenHeld(ctx, pages, tctx, in, width, height)
		if err != nil {
			return "", err
		}
		if entry == nil {
			held, err := browser.OpenHeld(ctx, pool, browser.OpenOpts{
				URL: in.URL, ProjectDir: in.ProjectDir, Path: in.Path,
				Width: width, Height: height, Wait: in.Wait, Routes: in.Routes,
				CaptureScope: captureScope(tctx),
			})
			if err != nil {
				return "", mapBrowserReject(err)
			}
			if entry, err = pages.Open(ctx, tctx.SessionID, held); err != nil {
				return "", capacityReject(err, tctx.SessionID)
			}
		}
		if live != nil {
			live.Attach(ctx, preview.AttachOpts{
				ProjectID:          tctx.ProjectID,
				SessionID:          tctx.SessionID,
				ParentSessionID:    tctx.ParentSessionID,
				PageID:             entry.ID,
				AssistantMessageID: tctx.Invocation.MessageID,
				ToolCallID:         tctx.ToolCallID,
				Held:               entry.Held,
			})
		}
		payload, _ := surveyjson.Marshal(OpenResult{
			ID: entry.ID, FinalURL: entry.TargetURL, RoutesActive: len(in.Routes),
			LivePages: pages.List(tctx.SessionID), MaxPages: pages.MaxPages(),
		})
		return string(payload), nil
	}
}

// reopenHeld reloads the page already held for the target, or returns nil when there is none.
func reopenHeld(ctx context.Context, pages *pagesession.Registry, tctx tools.ToolContext, in pageOpenArgs, width, height int) (*pagesession.Entry, error) {
	target, err := browser.ResolveTarget(browser.CaptureRequest{URL: in.URL, ProjectDir: in.ProjectDir, Path: in.Path})
	if err != nil {
		return nil, nil //nolint:nilerr // An unresolvable target opens fresh and reports its own rejection.
	}
	liveID, ok := pages.FindByTarget(tctx.SessionID, target)
	if !ok {
		return nil, nil
	}
	entry, err := pages.RequireRunning(tctx.SessionID, liveID)
	if err != nil || entry == nil || entry.Held == nil {
		return nil, nil //nolint:nilerr // A page that stopped running is replaced by a fresh one.
	}
	if err := entry.Held.Reload(ctx, width, height, in.Wait, in.Routes); err != nil {
		return nil, mapBrowserReject(err)
	}
	return entry, nil
}

func capacityReject(err error, sessionID string) error {
	var capacity *pagesession.CapacityError
	if errors.As(err, &capacity) {
		return &toolrejection.ToolReject{
			Code: "PAGE_CAP_REACHED",
			Data: map[string]any{
				"max_pages":     capacity.Limit,
				"live_pages":    capacity.IDs,
				"page_live_ids": capacity.IDs,
				"session_id":    sessionID,
			},
		}
	}
	return err
}

func requirePage(pages *pagesession.Registry, sessionID, id string) (*pagesession.Entry, error) {
	entry, err := pages.RequireRunning(sessionID, id)
	if err != nil {
		return nil, mapPageLifecycleReject(err, id)
	}
	return entry, nil
}

func mapPageLifecycleReject(err error, id string) error {
	code := "PAGE_NOT_FOUND"
	reason := "not_found"
	if errors.Is(err, pagesession.ErrPageNotRunning) {
		code = "PAGE_NOT_RUNNING"
		reason = "not_running"
	}
	return &toolrejection.ToolReject{Code: code, Data: map[string]any{"id": id, "reason": reason}}
}

// attachPageVisual makes a page result's raster or recording the tool's visual. A timeline
// is perceived as its contact sheet.
func attachPageVisual(tctx tools.ToolContext, out browser.CaptureResult) {
	if tctx.Out == nil {
		return
	}
	tctx.Out.Visual = &tools.VisualCapture{
		Mime:      out.Mime,
		Bytes:     append([]byte(nil), out.Bytes...),
		Source:    api.VisualArtifactSourceCapture,
		Caption:   out.Caption,
		Perceive:  out.Mime != browser.FilmstripMime,
		Projected: true,
		Width:     out.Width,
		Height:    out.Height,
	}
}

// mapBrowserReject carries a browser's structured rejection to the tool result.
func mapBrowserReject(err error) error {
	rej := &browserengine.RejectError{}
	if errors.As(err, &rej) {
		return &toolrejection.ToolReject{Code: rej.Code, Data: rej.Data}
	}
	return err
}

// recordedFailure carries a drive's rejection with the recording it still produced: the
// archive becomes the call's visual and the rejection names the timeline, so the frames
// before the failed step stay readable.
func recordedFailure(tctx tools.ToolContext, out browser.CaptureResult, err error) error {
	if len(out.Bytes) == 0 || out.Timeline == nil {
		return mapBrowserReject(err)
	}
	attachPageVisual(tctx, out)
	rej := &browserengine.RejectError{}
	if !errors.As(err, &rej) {
		return err
	}
	data := make(map[string]any, len(rej.Data)+2)
	for k, v := range rej.Data {
		data[k] = v
	}
	data["timeline"] = out.Timeline
	data["recorded"] = true
	return &toolrejection.ToolReject{Code: rej.Code, Data: data}
}
