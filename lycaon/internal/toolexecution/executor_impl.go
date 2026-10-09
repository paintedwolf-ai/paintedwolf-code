package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolsecrets"

	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// networkEgressTool is the synthetic ProposedAction.Tool for an egress approval.
const networkEgressTool = "network"

// Invoke runs policy checks then delegates to the tool registry.
func (e *Executor) Invoke(ctx context.Context, qualifiedName string, args map[string]any, tc tools.ToolContext) (out string, err error) {
	tc.BeginInvocation()
	defer func() { tc.Effects.Secrets.Finish(ctx) }()
	// Exits render their own rejects; this is the backstop for one that does not.
	defer func() {
		err = e.Rejections.renderUnrenderedReject(qualifiedName, err)
		if tc.Effects.Out != nil && strings.HasPrefix(qualifiedName, "mcp_") {
			tc.Effects.Out.Facts.ProviderErrorCode = oar.MCPMachineErrorCode(err)
		}
	}()
	// Hold one MCP generation through approval and dispatch.
	releaseDefinition := e.Metadata.registry.LeasePrefix(qualifiedName, "mcp_")
	defer releaseDefinition()
	profileID := tc.Identity.Agent
	if profileID == "" {
		profileID = e.defaultProfile
	}
	tc.Identity.Agent = profileID
	ctx = people.WithoutCaller(ctx)
	ctx = tools.WithRecoveryTools(ctx, tc.Turn.TurnOfferedToolNames)
	ctx = tools.SandboxScopeContext(ctx, tc)
	ctx = authzledger.WithInvocation(ctx, tc.Identity.SessionID, tc.Identity.ParentSessionID, tc.Identity.ToolCallID)
	sessionFacts := curationctx.SessionFrom(ctx)
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       tc.Identity.SessionID,
		OwnerPersonID:   sessionFacts.OwnerPersonID,
		Posture:         sessionFacts.Posture,
		ProjectID:       tc.Identity.ProjectID,
		Agent:           tc.Identity.Agent,
		ParentSessionID: tc.Identity.ParentSessionID,
		ToolCallID:      tc.Identity.ToolCallID,
		ProjectDir:      tc.ActiveRootPath(),
	})
	if reject := e.validateInvocation(ctx, qualifiedName, profileID, args, &tc); reject != nil {
		return "", e.Rejections.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, reject)
	}
	secretUse, parseErr := parseSecretUse(tc.Invocation.Contract, args)
	if parseErr != nil {
		var reject *toolrejection.ToolReject
		if errors.As(parseErr, &reject) {
			return "", e.Rejections.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, reject)
		}
		return "", e.Rejections.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"tool": qualifiedName, "reason": parseErr.Error()}))
	}
	ctx = withSecretUse(ctx, secretUse)
	args, tc, err = e.applyPreInvokeBoundary(ctx, qualifiedName, profileID, args, tc)
	if err != nil {
		return "", err
	}
	canonicalArgs := args
	// Handlers execute against resolved arguments and echo against these.
	tc.Effects.CanonicalArgs = canonicalArgs
	ctx = secretcap.WithResolution(ctx, tc.Effects.Secrets)
	executionArgs := tc.Effects.Secrets.Arguments
	if err := e.Secrets.screenArgvSecrets(ctx, qualifiedName, executionArgs, tc); err != nil {
		return "", err
	}
	if err := e.Secrets.screenFileSecrets(ctx, qualifiedName, executionArgs, tc); err != nil {
		return "", err
	}
	// The screened save is a file tool's transport.
	if tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		if err := tc.Effects.Secrets.HandOff(ctx, nil); err != nil {
			return "", e.Rejections.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, toolrejection.HeldHandOffReject(string(secretmatch.SurfaceFile), err))
		}
	}
	tc.Execution.ProcessReview = e.Process.processReviewer(qualifiedName, canonicalArgs, tc)
	tc.Files.FileChangeReview = e.Boundary.fileChangeReviewer(qualifiedName, canonicalArgs, tc)
	if e.Rejections.blockPlane != nil {
		if err := e.Rejections.blockPlane.Evaluate(ctx, oar.AnchorToolPreInvoke, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return "", err
		}
		if err := e.Rejections.blockPlane.Evaluate(ctx, oar.AnchorToolHandler, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return "", err
		}
	}
	out, err = e.Metadata.registry.Run(ctx, qualifiedName, executionArgs, tc)
	// A consumer may echo what it received; results carry references, never resolved values.
	out = tc.Effects.Secrets.ReferenceEchoes(out)
	err = toolsecrets.ReferenceEchoesInError(tc.Effects.Secrets, err)
	if errors.Is(err, context.Canceled) {
		return out, err
	}
	capabilityDenied := e.Rejections.takeCapabilityDenial(tc.Identity.SessionID, tc.Identity.ToolCallID)

	if err != nil {
		if capabilityDenied && toolrejection.AsToolReject(err) == nil {
			return out, fmt.Errorf("%w\n\n%s", err, e.Approvals.outcomeMessage(approvaloutcome.CodeApprovalDenied, nil))
		}
		if host := toolrejection.HostRefusal(err); host != nil {
			return out, host
		}
		displayCmd := commandsurface.PrimaryCommandLine(canonicalArgs, nil)
		if tr := toolrejection.AsToolReject(err); tr != nil {
			return out, e.Rejections.settleReject(ctx, qualifiedName, profileID, canonicalArgs, tr)
		}
		owner := tc.Invocation.Contract.Owner
		if tr := toolrejection.GitFailureObservation(err); tr != nil {
			return "", e.Rejections.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				toolrejection.CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := toolcommand.ArgvShapeObservation(qualifiedName, err); tr != nil {
			return "", e.Rejections.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				toolrejection.CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := toolrejection.CommandSurfaceObservation(qualifiedName, profileID, displayCmd, canonicalArgs,
			toolschema.ArgFieldPaths(e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, qualifiedName)), err); tr != nil {
			return "", e.Rejections.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				toolrejection.CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := toolrejection.ScopeObservation(profileID, tc.Turn.TurnSurfaceID, qualifiedName, toolrejection.PathFromToolArgs(canonicalArgs), err); tr != nil {
			return "", e.Rejections.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				toolrejection.CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		ownerRef := tc.Invocation.Contract.Owner
		if tc.Effects.Out != nil && strings.TrimSpace(tc.Effects.Out.OwnerRef) != "" {
			ownerRef = tc.Effects.Out.OwnerRef
		}
		tr := toolrejection.OwnerFailure(qualifiedName, ownerRef, err)
		if e.Rejections.blockPlane != nil {
			if ferr := e.Rejections.blockPlane.RejectObservation(ctx, qualifiedName, profileID, canonicalArgs, tr); ferr != nil {
				return out, ferr
			}
		}
		// Subsystem-owner output survives this failure.
		return out, e.Rejections.renderReject(tr)
	}
	return out, nil
}

func (e *Executor) validateInvocation(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	tc *tools.ToolContext,
) *toolrejection.ToolReject {
	if reject := tools.ValidateCallArguments(qualifiedName, args, e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, qualifiedName), *tc); reject != nil {
		return reject
	}
	if reject := e.exactCommandReplacementReject(ctx, qualifiedName, profileID, args, *tc); reject != nil {
		return reject
	}
	if reject := toolcommand.ArgvShapeReject(qualifiedName, args); reject != nil {
		return reject
	}
	if qualifiedName == "command" || qualifiedName == "verify" {
		_, parseErr := commandsurface.ParsePlan(args)
		if reject := toolrejection.CommandSurfaceObservation(qualifiedName, profileID, commandsurface.PrimaryCommandLine(args, nil), args, toolschema.ArgFieldPaths(e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, qualifiedName)), parseErr); reject != nil {
			return reject
		}
	}
	if reject := tools.ValidateCapabilityPathAuthority(args, tc.Host.SessionScratchDir); reject != nil {
		return reject
	}

	// Freeze the registry contract when the caller did not lease one.
	// Dynamic tools are absent from the compiled catalog.
	if strings.TrimSpace(tc.Invocation.Contract.Owner) == "" {
		def, ok := e.definitionFor(qualifiedName)
		if !ok {
			return &toolrejection.ToolReject{
				Code:         toolrejection.ToolOwnerFailedCode,
				FailureClass: api.FailureClassOwnerError,
				Data: map[string]any{
					"tool":   qualifiedName,
					"reason": "no registered definition to freeze for dispatch",
				},
			}
		}
		tc.Invocation.Contract = def.Contract
	}
	return capabilityrequest.ValidateCapabilityContract(tc.Invocation.Contract, args)
}

func (e *Executor) exactCommandReplacementReject(
	ctx context.Context,
	toolName, profileID string,
	args map[string]any,
	tc tools.ToolContext,
) *toolrejection.ToolReject {
	if !toolcommand.IsCommandEquivalenceRunner(toolName) {
		return nil
	}
	schema := e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, toolName)
	envelope, ok := toolcommand.ParseEnvelope(toolcommand.ArgsWithoutDefaults(args, schema))
	if !ok {
		return nil
	}
	calls, ok := envelope.Replacements(ctx, tc.ActiveRootPath(), tc.Host.SessionScratchDir)
	if !ok {
		return nil
	}
	for _, call := range calls {
		if _, callable := e.definitionFor(call.Tool); !callable {
			return nil
		}
		decision, _ := tools.EvaluateListVisible(ctx, e.Metadata.policy, platform.PolicyContext{
			ProfileID: profileID, ToolAccess: tc.Turn.ToolAccess, ToolName: call.Tool,
		})
		if decision == nil || !decision.Allowed {
			return nil
		}
		schema := e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, call.Tool)
		if schema == nil {
			return nil
		}
		argsValid := tools.ValidateToolArgs(schema, call.Args) == nil
		if !argsValid {
			return nil
		}
	}
	return toolcommand.ReplacementReject(envelope.Command, profileID, calls)
}

// definitionFor reads the registry snapshot dispatch will run.
func (e *Executor) definitionFor(qualifiedName string) (tools.Definition, bool) {
	if e == nil || e.Metadata.registry == nil {
		return tools.Definition{}, false
	}
	return e.Metadata.registry.Definition(qualifiedName)
}

func (e *Executor) prepareWorkerBranch(ctx context.Context, tool, profileID string, args map[string]any, tc tools.ToolContext) (tools.ToolContext, error) {
	prepared, err := e.ensureWorkerBranchIfNeeded(ctx, tool, tc)
	if err == nil {
		return prepared, nil
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		reject = &toolrejection.ToolReject{Code: "WORKER_BRANCH_CLAIM_FAILED", Data: map[string]any{"tool": tool, "reason": err.Error()}}
	}
	reject = toolrejection.CompleteFailureMetadata(reject, tool, tc.Invocation.Contract.Owner)
	return tc, e.Rejections.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
}

func (e *Executor) ensureWorkerBranchIfNeeded(ctx context.Context, tool string, tc tools.ToolContext) (tools.ToolContext, error) {
	if !tools.RequiresWorkerBranch(tool) {
		return tc, nil
	}
	if strings.TrimSpace(tc.Identity.WorkerJobID) == "" || strings.TrimSpace(tc.Source.WorkerBranchRoot) != "" {
		return tc, nil
	}
	if tc.Source.WorkerCoord == nil {
		return tc, &toolrejection.ToolReject{
			Code: "WORKER_BRANCH_CLAIM_FAILED",
			Data: map[string]any{"tool": tool, "reason": "worker branch coordinator not configured"},
		}
	}
	return tc.Source.WorkerCoord.EnsureWorkerBranch(ctx, tc)
}
