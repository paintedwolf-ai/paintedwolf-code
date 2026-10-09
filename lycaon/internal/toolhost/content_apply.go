package toolhost

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// PhaseContentReview describes active workflow phase edit-review policy.
type PhaseContentReview struct {
	Tools []string
	Paths []string
}

// SessionContentReviewSource returns phase-scoped content review when a workflow run is active.
type SessionContentReviewSource interface {
	PhaseContentReview(ctx context.Context, sessionID string) (*PhaseContentReview, error)
}

// ContentApplyService gates filesystem writes behind content_apply checkpoints.
type ContentApplyService struct {
	Mgr      hitl.CheckpointManager
	Review   *settings.ReviewStore
	PhaseSrc SessionContentReviewSource
}

// RequiresReview reports whether tool/path needs content_apply under merged policy.
func (s *ContentApplyService) RequiresReview(ctx context.Context, sessionID, projectDir, tool, path string) bool {
	if s == nil || s.Mgr == nil || s.Review == nil {
		return false
	}
	cfg := s.Review.Get(llm.SettingsScopeProject, projectDir)
	if toolcontract.MutatesContent(tool) && cfg.MatchesReviewPath(tool, path) {
		return true
	}
	if s.PhaseSrc != nil {
		if phase, err := s.PhaseSrc.PhaseContentReview(ctx, sessionID); err == nil && phase != nil {
			if phaseRequiresReview(phase, tool, path) {
				return true
			}
		}
	}
	return false
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func phaseRequiresReview(phase *PhaseContentReview, tool, path string) bool {
	if phase == nil {
		return false
	}
	if len(phase.Tools) > 0 {
		match := false
		for _, t := range phase.Tools {
			if t == tool {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	if len(phase.Paths) > 0 {
		for _, p := range phase.Paths {
			if reviewGlobMatch(p, path) {
				return true
			}
		}
		return false
	}
	return true
}

func reviewGlobMatch(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	path = strings.TrimSpace(path)
	if pattern == "" || pattern == "**" || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return pattern == path
}

// GateApply blocks until the user resolves content_apply, then returns the one
// host-composed byte sequence the native tool may write.
func (s *ContentApplyService) GateApply(ctx context.Context, tool, path string, before *string, after string, tctx tools.ToolContext) (string, error) {
	if !s.RequiresReview(ctx, tctx.Identity.SessionID, tctx.ActiveRootPath(), tool, path) {
		return after, nil
	}
	// The invocation's content decisions key on the resolved destination.
	reviewedPath := ""
	if resolved, err := projectpaths.ResolveWrite(ctx, nil, tctx, path); err == nil {
		reviewedPath = resolved.Abs
	} else if tctx.Files.FileChangeReview != nil {
		return "", err
	}
	// The person reviews the change with managed values echoed as their
	// references; that same text keys the decision and the gate coverage.
	maskedAfter := tctx.Effects.Secrets.ReferenceEchoes(after)
	var maskedBefore *string
	if before != nil {
		mb := tctx.Effects.Secrets.ReferenceEchoes(*before)
		maskedBefore = &mb
	}
	if reviewedPath != "" {
		if final, ok := tctx.ContentDecision(reviewedPath, stringOrEmpty(maskedBefore), maskedAfter); ok {
			return tctx.Effects.Secrets.Substitute(final)
		}
	}

	payload := &hitl.ContentApplyPayload{
		Tool:       tool,
		ToolCallID: tctx.Identity.ToolCallID,
		Path:       path,
		Before:     maskedBefore,
		After:      maskedAfter,
	}
	resp, err := s.Mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID:    tctx.Identity.SessionID,
		Kind:         api.CheckpointKindContentApply,
		Title:        fmt.Sprintf("Review edit: %s", path),
		ProjectID:    tctx.Identity.ProjectID,
		ContentApply: payload,
	})
	if err != nil {
		return "", err
	}
	release := tools.HoldForApproval(tctx.Effects.Presence, resp.CheckpointID)
	final, err := hitl.WaitForCheckpoint(ctx, s.Mgr, resp.CheckpointID)
	release()
	if err != nil {
		return "", err
	}
	if final.ContentResult == nil {
		switch final.Status {
		case hitl.DecisionStatusExpired:
			return "", fmt.Errorf("%s", hitl.ErrContentApplyExpired)
		case hitl.DecisionStatusCanceled:
			return "", context.Canceled
		default:
			return "", fmt.Errorf("%s", hitl.ErrContentApplyUnresolved)
		}
	}
	switch final.ContentResult.Decision {
	case api.ContentApplyReject:
		reject := &toolrejection.ToolReject{
			Code: "CONTENT_APPLY_REJECTED",
			Data: map[string]any{"path": filepath.ToSlash(strings.TrimSpace(path))},
		}
		toolrejection.AttachUserGuidance(reject, final.ContentResult.Guidance)
		return "", reject
	case api.ContentApplyApprove, api.ContentApplyApprovePartial:
		if reviewedPath != "" {
			tctx.RecordContentApproval(reviewedPath, stringOrEmpty(maskedBefore), maskedAfter, final.ContentResult.FinalAfter)
		}
		return tctx.Effects.Secrets.Substitute(final.ContentResult.FinalAfter)
	default:
		return "", fmt.Errorf("%s", hitl.ErrContentApplyUnresolved)
	}
}
