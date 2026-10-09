package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// OverlayToolDeps provides overlay merge operations.
type OverlayToolDeps struct {
	Merge *MergeService
}

// RegisterOverlayTools registers coordinator overlay tools.
func RegisterOverlayTools(reg *tools.DefaultRegistry, deps OverlayToolDeps) error {
	if reg == nil || deps.Merge == nil {
		return fmt.Errorf("registry and overlay merge service required")
	}
	if err := reg.Register("promote_overlay", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		var overrideErr error
		ctx, overrideErr = tools.WithSyntaxOverride(ctx, args)
		if overrideErr != nil {
			return "", overrideErr
		}
		overlayID := strings.TrimSpace(stringArg(args["overlay_id"]))
		if overlayID == "" {
			return "", fmt.Errorf("overlay_id is required")
		}
		captureWorkerSubject(tctx, deps.Merge.Queue, overlayID)
		resolutions := parsePromoteResolutions(args["resolutions"])
		// A folder drop applies to every changed file below it.
		for _, p := range parseStringListArg(args["drop"]) {
			if p = strings.TrimSpace(p); p != "" {
				resolutions = append(resolutions, api.WorkerPromoteResolution{
					Path:   p,
					Action: api.WorkerPromoteResolutionActionDrop,
				})
			}
		}
		in := api.PromoteOverlayInput{
			Detail:      parsePromoteDetailArg(args["detail"]),
			Resolutions: resolutions,
			ToolCallID:  tctx.Identity.ToolCallID,
			UserTurn:    tctx.Identity.UserTurn,
		}
		out, err := deps.Merge.PromoteOverlay(ctx, tctx.Identity.SessionID, overlayID, in)
		if err == nil {
			tools.ReportSyntaxOverride(ctx, tctx, out.Applied...)
		}
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.OwnerRef = strings.TrimSpace(out.JobID)
			tctx.Effects.Out.Completion = overlayCompletion("overlay_promotion", out, "promoted")
			tctx.Effects.Out.OverlayPromotion = out.OverlayPromotion
		}
		return formatOverlayPromoteHostContent(ctx, overlayID, out, err)
	}); err != nil {
		return err
	}
	if err := reg.Register("reject_overlay", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		overlayID := strings.TrimSpace(stringArg(args["overlay_id"]))
		if overlayID == "" {
			return "", fmt.Errorf("overlay_id is required")
		}
		captureWorkerSubject(tctx, deps.Merge.Queue, overlayID)
		reason := strings.TrimSpace(stringArg(args["reason"]))
		out, err := deps.Merge.RejectOverlay(ctx, tctx.Identity.SessionID, overlayID, reason)
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.OwnerRef = strings.TrimSpace(out.OverlayID)
			tctx.Effects.Out.Completion = &api.ToolCompletion{
				Operation: "overlay_rejection", State: "rejected", ResourceKind: "overlay", ResourceID: strings.TrimSpace(out.OverlayID),
			}
		}
		return formatOverlayRejectHostContent(ctx, overlayID, out, err)
	}); err != nil {
		return err
	}
	if err := reg.Register("preview_overlay", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		overlayID := strings.TrimSpace(stringArg(args["overlay_id"]))
		if overlayID == "" {
			return "", fmt.Errorf("overlay_id is required")
		}
		captureWorkerSubject(tctx, deps.Merge.Queue, overlayID)
		detail := parsePromoteDetailArg(args["detail"])
		paths := parseOverlayPathFilter(args["path"])
		out, err := deps.Merge.PreviewForSession(ctx, tctx.Identity.SessionID, overlayID, detail, paths)
		if err != nil {
			return "", err
		}
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.OwnerRef = strings.TrimSpace(out.JobID)
			tctx.Effects.Out.Completion = overlayCompletion("overlay_preview", out, "previewed")
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}

func overlayCompletion(operation string, result api.WorkerMergeResult, fallbackState string) *api.ToolCompletion {
	state := strings.TrimSpace(string(result.Status))
	if state == "" {
		state = fallbackState
	}
	return &api.ToolCompletion{
		Operation: operation, State: state, ResourceKind: "overlay", ResourceID: strings.TrimSpace(result.JobID),
	}
}

func stringArg(raw any) string {
	s, _ := raw.(string)
	return s
}

func parseOverlayPathFilter(raw any) []string {
	path, ok := raw.(string)
	if !ok {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return []string{path}
}

func parseStringListArg(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		s, _ := item.(string)
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parsePromoteDetailArg(raw any) string {
	s, _ := raw.(string)
	return normalizePromoteDetail(s)
}

func parsePromoteResolutions(raw any) []api.WorkerPromoteResolution {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []api.WorkerPromoteResolution
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		out = append(out, api.WorkerPromoteResolution{
			Path:    path,
			Action:  parsePromoteResolutionAction(m["action"]),
			Content: content,
			Hunks:   parsePromoteHunkResolutions(m["hunks"]),
		})
	}
	return out
}

func parsePromoteHunkResolutions(raw any) []api.WorkerPromoteHunkResolution {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []api.WorkerPromoteHunkResolution
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start := intArg(m["start_line"])
		end := intArg(m["end_line"])
		content, _ := m["content"].(string)
		if start < 1 {
			continue
		}
		out = append(out, api.WorkerPromoteHunkResolution{
			StartLine: start,
			EndLine:   end,
			Content:   content,
		})
	}
	return out
}

func parsePromoteResolutionAction(raw any) api.WorkerPromoteResolutionAction {
	s, _ := raw.(string)
	return api.WorkerPromoteResolutionAction(strings.TrimSpace(s))
}

func intArg(raw any) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func formatOverlayPromoteHostContent(ctx context.Context, overlayID string, out api.WorkerMergeResult, runErr error) (string, error) {
	banners, err := guidance.RenderOverlayRebaseConflictBanners(ctx, overlayID, out.Rebased)
	if err != nil {
		return "", err
	}
	content, err := session.FormatOverlayHostToolContent(hostmarker.OverlayPromoteEventPrefix, out, banners)
	if err != nil {
		return "", err
	}
	if runErr == nil {
		return content, nil
	}
	if !hasPromoteRejectPayload(out) {
		return "", runErr
	}
	return content, runErr
}

func formatOverlayRejectHostContent(ctx context.Context, overlayID string, out api.OverlayRejectOutcome, runErr error) (string, error) {
	banners, err := guidance.RenderOverlayParentRejectedBanners(ctx, overlayID, out.Orphaned)
	if err != nil {
		return "", err
	}
	content, err := session.FormatOverlayHostToolContent(hostmarker.OverlayRejectEventPrefix, out, banners)
	if err != nil {
		return "", err
	}
	if runErr == nil {
		return content, nil
	}
	if strings.TrimSpace(out.OverlayID) == "" {
		return "", runErr
	}
	return content, runErr
}

func hasPromoteRejectPayload(out api.WorkerMergeResult) bool {
	if strings.TrimSpace(out.JobID) == "" {
		return false
	}
	return len(out.Conflicts) > 0 ||
		strings.TrimSpace(out.SpillPath) != "" ||
		len(out.CleanPaths) > 0 ||
		len(out.Paths) > 0 ||
		len(out.OverlapJobIDs) > 0
}
