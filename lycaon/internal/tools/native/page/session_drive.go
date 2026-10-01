package page

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/timelinearchive"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ActHandler builds the page_act handler. With record, the drive is also recorded as a
// timeline the model perceives as its contact sheet.
func ActHandler(pages *pagesession.Registry, live LivePreview) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parsePageActArgs(args)
		if err != nil {
			return "", err
		}
		if err := resolveDriveFixtures(ctx, tctx, nil, in.Actions); err != nil {
			return "", err
		}
		if live != nil {
			live.Claim(ctx, tctx.SessionID, in.ID, tctx.Invocation.MessageID, tctx.ToolCallID)
		}
		entry, err := requirePage(pages, tctx.SessionID, in.ID)
		if err != nil {
			return "", err
		}
		if live != nil {
			live.PublishDriving(ctx, tctx.SessionID, in.ID, true)
			defer live.PublishDriving(ctx, tctx.SessionID, in.ID, false)
		}
		onAction := func(act browser.CaptureAction, raw json.RawMessage) {
			if live != nil {
				live.PublishAction(ctx, tctx.SessionID, in.ID, act, raw)
			}
		}
		result := pageActResult{ID: in.ID}
		if in.Record != nil {
			rec, err := entry.Held.Record(ctx, in.Actions, *in.Record, onAction)
			recorded := browser.CaptureResult{
				Mime: timelinearchive.Mime, Bytes: rec.Archive, Width: entry.Held.Width, Height: entry.Held.Height,
				Coverage: rec.Coverage, Timeline: rec.Report(),
			}
			if err != nil {
				return "", recordedFailure(tctx, recorded, err)
			}
			attachPageVisual(tctx, recorded)
			result.ActionResults, result.PageEvidence, result.RoutesActive = rec.Results, rec.Evidence, rec.RoutesActive
			result.Timeline, result.Coverage = recorded.Timeline, rec.Coverage
		} else {
			ctx, cancel := context.WithTimeout(ctx, browser.RasterizeTimeout)
			defer cancel()
			report, err := entry.Held.Act(ctx, in.Actions, onAction)
			if err != nil {
				return "", mapBrowserReject(err)
			}
			result.ActionResults, result.PageEvidence, result.RoutesActive = report.Results, report.Evidence, report.RoutesActive
		}
		result.LivePages = pages.List(tctx.SessionID)
		payload, _ := surveyjson.Marshal(result)
		return string(payload), nil
	}
}

// SnapshotHandler builds the page_snapshot handler.
func SnapshotHandler(pages *pagesession.Registry, live LivePreview) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parsePageSnapshotArgs(args)
		if err != nil {
			return "", err
		}
		if live != nil {
			live.Claim(ctx, tctx.SessionID, in.ID, tctx.Invocation.MessageID, tctx.ToolCallID)
		}
		entry, err := requirePage(pages, tctx.SessionID, in.ID)
		if err != nil {
			return "", err
		}
		out, err := entry.Held.Snapshot(ctx, browser.SnapshotOpts{Selector: in.Selector, Caption: in.Caption})
		if err != nil {
			return "", mapBrowserReject(err)
		}
		attachPageVisual(tctx, out)
		result := universalResult(in.ID, out, in.Caption)
		result.LivePages = pages.List(tctx.SessionID)
		payload, _ := surveyjson.Marshal(result)
		return string(payload), nil
	}
}

// CloseHandler builds the page_close handler.
func CloseHandler(pages *pagesession.Registry, live LivePreview) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		in, err := parsePageCloseArgs(args)
		if err != nil {
			return "", err
		}
		if live != nil {
			live.Claim(ctx, tctx.SessionID, in.ID, tctx.Invocation.MessageID, tctx.ToolCallID)
		}
		entry, err := requirePage(pages, tctx.SessionID, in.ID)
		if err != nil {
			return "", err
		}
		result := pageUniversalResult{ID: in.ID}
		if in.Snapshot {
			out, serr := entry.Held.Snapshot(ctx, browser.SnapshotOpts{Selector: in.Selector, Caption: in.Caption})
			if serr != nil {
				return "", mapBrowserReject(serr)
			}
			attachPageVisual(tctx, out)
			result = universalResult(in.ID, out, in.Caption)
		}
		if err := pages.ClosePage(ctx, tctx.SessionID, in.ID); err != nil {
			return "", mapPageLifecycleReject(err, in.ID)
		}
		result.Closed = true
		result.LivePages = pages.List(tctx.SessionID)
		payload, _ := surveyjson.Marshal(result)
		return string(payload), nil
	}
}

func universalResult(id string, out browser.CaptureResult, caption string) pageUniversalResult {
	return pageUniversalResult{
		ID: id, State: out.State, Snapshot: out.Snapshot, PageEvidence: out.PageEvidence,
		RoutesActive: out.RoutesActive, Mime: out.Mime, Width: out.Width, Height: out.Height,
		Caption: strings.TrimSpace(caption), FinalURL: out.FinalURL,
	}
}
