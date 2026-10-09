package toolfeedback

import (
	"github.com/lycaon/lycaon/internal/tools"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
)

// BlockPlane evaluates policy decisions and renders typed rejections without changing their codes.
type BlockPlane struct {
	Pipeline   *oar.GuardPipeline
	Renderer   *oar.Renderer
	MCPCatalog oar.MCPCatalogView // optional; fills MCP structural facts
}

func fillOccurrenceIdentity(ctx context.Context, gc *oar.GuardContext) {
	if gc == nil {
		return
	}
	tools.RegisterRecoveryFacts(ctx, gc)
	sess := curationctx.SessionFrom(ctx)
	gc.Session.SessionID = sess.SessionID
	gc.Session.Principal = sess.OwnerPersonID
	gc.Session.SessionPosture = sess.Posture
	if caller, ok := people.Caller(ctx); ok {
		gc.Session.Principal = caller.ID
		gc.Session.PrincipalRoles = []string{string(caller.Role)}
	}
}

// Enforces reports whether catalog anchor is under OAR block-plane authority.
func (bp *BlockPlane) Enforces(anchor string) bool {
	return bp != nil && bp.Pipeline != nil && bp.Pipeline.AnchorEnforced(anchor)
}

// Evaluate runs EvaluateBlock after optional observation fill (MCP pre/post).
// Returns a formatted reject when OAR fires a block Decision.
func (bp *BlockPlane) Evaluate(ctx context.Context, anchor, tool, profile string, args map[string]any, observe func(*oar.GuardContext) error) error {
	if bp == nil || bp.Pipeline == nil {
		return nil
	}
	if !bp.Pipeline.AnchorEnforced(anchor) {
		return nil
	}
	gc := oar.NewGuardContext()
	fillOccurrenceIdentity(ctx, gc)
	gc.ObserveToolCall(tool, args)
	gc.Session.Profile = profile
	gc.Session.PermissionProfile = profile
	gc.Progress.VerifyHasCommand = commandsurface.HasCommandInput(args)
	gc.DeriveToolClassFacts()
	if observe != nil {
		if err := observe(gc); err != nil {
			return err
		}
	} else if bp.MCPCatalog != nil {
		oar.ObserveMCPStructuralPre(gc, tool, bp.MCPCatalog)
	}
	res, err := bp.Pipeline.EvaluateBlock(ctx, anchor, gc)
	if err != nil {
		return err
	}
	if res == nil || !res.Enforced || res.Decision == nil {
		return nil
	}
	if res.Decision.Effect != oar.EffectBlock {
		return nil
	}
	if bp.Renderer == nil {
		if d := strings.TrimSpace(res.Decision.Code); d != "" {
			return toolrejection.FormatDecisionReject(d, nil, nil)
		}
		return nil
	}
	rendered, rerr := bp.Renderer.Render(ctx, oar.StageFromAnchor(anchor), res.Decision)
	if rerr != nil {
		return rerr
	}
	if rd, ok := oar.FirstBlock(rendered); ok && rd.Text != "" {
		return guidance.NewRefusal(rd.Decision.Code, rd.Text).WithPolicyCopy(rd.Decision.Copy)
	}
	if d := strings.TrimSpace(res.Decision.Code); d != "" {
		return toolrejection.FormatDecisionReject(d, nil, nil)
	}
	return nil
}

// RejectFromObservation runs EvaluateBlock for anchor with ToolReject as facts.
func (bp *BlockPlane) RejectFromObservation(ctx context.Context, anchor, tool, profile string, args map[string]any, tr *toolrejection.ToolReject) error {
	if bp == nil || bp.Pipeline == nil || tr == nil {
		return nil
	}
	if !bp.Pipeline.AnchorEnforced(anchor) {
		return nil
	}
	gc := oar.NewGuardContext()
	fillOccurrenceIdentity(ctx, gc)
	gc.ObserveToolCall(tool, args)
	gc.Session.Profile = profile
	gc.Session.PermissionProfile = profile
	gc.Progress.VerifyHasCommand = commandsurface.HasCommandInput(args)
	if bp.MCPCatalog != nil {
		oar.ObserveMCPStructuralPre(gc, tool, bp.MCPCatalog)
	}
	ApplyToolRejectObservations(gc, tr)
	res, err := bp.Pipeline.EvaluateBlock(ctx, anchor, gc)
	if err != nil {
		if gc.ObservedRejectCode == "" {
			return err
		}
		return toolrejection.RenderRejectBlock(gc.ObservedRejectCode, tr.Data, tr, nil)
	}
	if res == nil || !res.Enforced || res.Decision == nil {
		return nil
	}
	if gc.ObservedRejectCode != "" && res.Decision.Code != gc.ObservedRejectCode {
		return toolrejection.RenderRejectBlock(gc.ObservedRejectCode, tr.Data, tr, nil)
	}
	if res.Decision.Effect != oar.EffectBlock {
		return nil
	}
	if bp.Renderer == nil {
		if d := strings.TrimSpace(res.Decision.Code); d != "" {
			return toolrejection.RenderRejectBlock(d, tr.Data, tr, nil)
		}
		return nil
	}
	rendered, rerr := bp.Renderer.Render(ctx, oar.StageFromAnchor(anchor), res.Decision)
	if rerr != nil {
		if gc.ObservedRejectCode == "" {
			return rerr
		}
		return toolrejection.RenderRejectBlock(gc.ObservedRejectCode, tr.Data, tr, nil)
	}
	if rd, ok := oar.FirstBlock(rendered); ok && rd.Text != "" {
		return guidance.NewRefusal(rd.Decision.Code, rd.Text).WithPolicyCopy(rd.Decision.Copy).WithDetails(tr.Data, nil).WithCause(tr)
	}
	if d := strings.TrimSpace(res.Decision.Code); d != "" {
		return toolrejection.RenderRejectBlock(d, tr.Data, tr, nil)
	}
	return nil
}

// applyToolRejectObservations publishes ToolReject as GuardContext facts.
func ApplyToolRejectObservations(gc *oar.GuardContext, tr *toolrejection.ToolReject) {
	if gc == nil || tr == nil {
		return
	}
	code := strings.TrimSpace(tr.Code)
	gc.ObservedRejectCode = code
	// [OAR-PROF-3] A handler or host-state failure is not an argument check.
	if tr.ArgumentValidation {
		gc.Invocation.ArgValidationErrors = AppendUnique(gc.Invocation.ArgValidationErrors, code)
		gc.Invocation.ArgValidationReason, _ = tr.Data["reason"].(string)
		gc.Invocation.ArgValidationField, _ = tr.Data["field"].(string)
	}
	if code != "" {
		gc.PutRejectData(code, tr.Data)
	}
	// MCP rules branch on the structured machine code.
	if strings.HasPrefix(strings.TrimSpace(gc.Invocation.Tool), "mcp_") {
		if mc := tr.MachineErrorCode(); mc != "" {
			gc.MCP.MCPErrorCode = mc
			gc.MCP.MCPCallOK = false
			shared := map[string]any{}
			for k, v := range tr.Data {
				shared[k] = v
			}
			shared["mcp_error_code"] = mc
			if strings.HasPrefix(mc, "MCP_SERVER_") {
				// Server codes use the namespace assigned at the MCP boundary.
				gc.ObservedRejectCode = "MCP_CALL_FAILED"
				gc.PutRejectData("MCP_CALL_FAILED", shared)
			} else {
				// A host refusal keeps its own decision, details, and recovery.
				gc.PutRejectData(code, shared)
			}
		}
	}
	obs := tr.ObservationToken()
	if obs == "" {
		return
	}
	applyObservationToken(gc, obs)
	if strings.HasPrefix(code, "USE_") {
		gc.Invocation.HabitRedirectMatch = code
	}
	switch code {
	case "COMMAND_NOT_ARGV":
		gc.Invocation.CommandNotArgv = true
		gc.Rejection.PolicyDenied = true
	case "TOOL_PROFILE_DENIED", "COORDINATOR_TOOL_DENIED":
		gc.Invocation.ToolAllowedForProfile = false
		gc.Rejection.PolicyDenied = true
	case "WRITE_SCOPE_DENIED", "COORDINATOR_READ_OUTSIDE_SCOPE", "COORDINATOR_INVESTIGATE_DENIED_PATH":
		gc.Access.PathOutsideScope = true
		gc.Rejection.PolicyDenied = true
	}
}

func applyObservationToken(gc *oar.GuardContext, obs string) {
	switch obs {
	case "is_directory":
		gc.Rejection.IsDirectory = true
	case "not_found":
		gc.Rejection.NotFound = true
	case "path_denied":
		gc.Rejection.PathDenied = true
	case "bulk_denied":
		gc.Rejection.BulkDenied = true
	case "binary_denied":
		gc.Rejection.BinaryDenied = true
	case "mode_denied":
		gc.Rejection.ModeDenied = true
	case "path_escape":
		gc.Rejection.PathEscape = true
	case "beyond_eof":
		gc.Rejection.BeyondEOF = true
	case "not_running":
		gc.Rejection.NotRunning = true
	case "unsupported":
		gc.Rejection.Unsupported = true
	case "resource_limit":
		gc.Rejection.ResourceLimit = true
	case "conflict":
		gc.Rejection.Conflict = true
	case "path_required":
		gc.Rejection.PathRequired = true
	case "id_required":
		gc.Rejection.IDRequired = true
	case "policy_denied":
		gc.Rejection.PolicyDenied = true
	case "unknown_target":
		gc.Rejection.UnknownTarget = true
	case "missing":
		gc.Rejection.Missing = true
	case "forbidden":
		gc.Rejection.Forbidden = true
	case "selector_empty":
		gc.Rejection.SelectorEmpty = true
	case "selector_ambiguous":
		gc.Rejection.SelectorAmbiguous = true
	default:
		// Agent anchors have no active tool profile.
		gc.Rejection.RejectObservation = obs
	}
}

func AppendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// RejectObservation evaluates the intrinsic rejection feedback occurrence.
func (bp *BlockPlane) RejectObservation(ctx context.Context, tool, profile string, args map[string]any, tr *toolrejection.ToolReject) error {
	return bp.RejectFromObservation(ctx, oar.AnchorToolRejected, tool, profile, args, tr)
}
