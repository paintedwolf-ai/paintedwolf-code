package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/pkgregistry"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// networkEgressTool is the synthetic ProposedAction.Tool for an egress approval.
const networkEgressTool = "network"

// DefaultToolExecutor routes tool invokes through policy then the registry.
type DefaultToolExecutor struct {
	policy             platform.PolicyEngine
	registry           *DefaultRegistry
	defaultProfile     string
	toolSchemas        *toolschema.Config
	schemaSource       func(ctx context.Context, sessionID string) *toolschema.Config
	checkpointMgr      hitl.CheckpointManager
	approvalGate       hitl.ApprovalGate
	approvalExplainer  ApprovalExplainer
	egressPostureFor   func(projectDir string) gate.Posture
	approvalOutcome    ApprovalOutcomeRenderer
	aiRationale        AIRationaleAttacher
	authzRecorder      authzledger.Recorder
	blockPlane         *BlockPlane
	approvalCoalesce   ToolApprovalCoalesce
	gateRepeat         GateRepeatLedger
	consequence        ConsequenceDeriver
	backgroundCommand  BackgroundCommandResolver
	hostResourceSource HostResourceConnectionSource
	socketRuntime      SocketCapabilityRuntime
	durableSockets     DurableSocketSource
	directIPRuntime    DirectIPCapabilityRuntime
	directIPLifecycle  DirectIPLifecycleHook
	approvalsDisabled  func(projectDir string) bool
	// presenceAvailableFn reports whether held values can be released at all.
	presenceAvailableFn func() bool
	secretMatcher       *secretmatch.Matcher
	secretIgnores       *projectignore.SecretService
	secretResolver      func(context.Context, map[string]any, secretcap.ResolveContext) (*secretcap.Resolution, error)
	secretReceiptOnce   sync.Once
	secretReceiptRT     *secretReceiptRuntime
	// secretExposure reads the session's credential-exposure fact.
	secretExposure func(ctx context.Context, chatSessionID string) (bool, error)
	// untrustedIngestion reads the session's external-content ingestion fact.
	untrustedIngestion func(ctx context.Context, chatSessionID string) (bool, error)
	// hostLedger is the chat's record of hosts it has already reached.
	hostLedger SessionHostLedger
	// egressGather holds a command's parked endpoints for one window so a
	// fan-out is one card, and remembers which hosts each command has asked for.
	egressGather     *egressGathers
	egressGatherOnce sync.Once
	// destinations answers whether the user's own configuration names a host.
	destinations *destconfig.Registry
	// registries names the public package registry serving a host.
	registries *pkgregistry.Catalog
	// capabilityDenials retains structured denials across boolean callbacks until the invocation returns.
	capabilityDenials sync.Map
	// sessionOverlay supplies chat write grants to review and execution.
	sessionOverlay SessionWriteRootOverlay
	// sessionListenGrant supplies chat listener authority to confinement.
	sessionListenGrant SessionListenGrant
	// sessionLoopbackGrant feeds chat-scoped local client authority into confinement.
	sessionLoopbackGrant SessionLoopbackGrant
	// localListenGate raises the pre-spawn card for an explicit listener ask.
	localListenGate LocalListenGate
	// loopbackConnectGate raises the pre-spawn card for a local client ask.
	loopbackConnectGate LoopbackConnectGate
	// localNetworkGate raises one card when one invocation widens both axes.
	localNetworkGate    LocalNetworkGate
	writeRootPreflight  WriteRootPreflight
	readPathPreflight   ReadPathPreflight
	packageExecution    *packageexec.Service
	packageExecutionErr error
	// sessionReadOverlay feeds the chat's protected-read leases into spawn inputs.
	sessionReadOverlay SessionReadPathOverlay
	// rejectFmt renders a reject the OAR block plane did not claim.
	rejectFmt *guidance.StaticRejectFormatter
}

func NewDefaultToolExecutor(policy platform.PolicyEngine, registry *DefaultRegistry, defaultProfile string) *DefaultToolExecutor {
	if defaultProfile == "" {
		defaultProfile = DefaultToolProfileID
	}
	packageExecution, packageExecutionErr := packageexec.NewService()
	return &DefaultToolExecutor{
		policy:              policy,
		registry:            registry,
		defaultProfile:      defaultProfile,
		packageExecution:    packageExecution,
		packageExecutionErr: packageExecutionErr,
	}
}

// Invoke runs policy checks then delegates to the tool registry.
func (e *DefaultToolExecutor) Invoke(ctx context.Context, qualifiedName string, args map[string]any, tc ToolContext) (out string, err error) {
	tc.ProcessControl, tc.HostExecution = false, false
	tc.executionPermit = nil
	tc.Secrets = nil
	defer func() { tc.Secrets.Finish(ctx) }()
	tc.ApprovedFileAccess = nil
	tc.PreparedFileAccess = nil
	tc.PolicyWriteGrants = nil
	tc.ProcessReview = nil
	tc.FileChangeReview = nil
	tc.contentReviews = &contentReviews{}
	// Exits render their own rejects; this is the backstop for one that does not.
	defer func() {
		err = e.renderUnrenderedReject(qualifiedName, err)
		if tc.Out != nil && strings.HasPrefix(qualifiedName, "mcp_") {
			tc.Out.Facts.ProviderErrorCode = oar.MCPMachineErrorCode(err)
		}
	}()
	// Hold one MCP generation through approval and dispatch.
	releaseDefinition := e.registry.LeasePrefix(qualifiedName, "mcp_")
	defer releaseDefinition()
	profileID := tc.Agent
	if profileID == "" {
		profileID = e.defaultProfile
	}
	tc.Agent = profileID
	ctx = people.WithoutCaller(ctx)
	ctx = WithRecoveryTools(ctx, tc.TurnOfferedToolNames)
	ctx = SandboxScopeContext(ctx, tc)
	ctx = authzledger.WithInvocation(ctx, tc.SessionID, tc.ParentSessionID, tc.ToolCallID)
	sessionFacts := curationctx.SessionFrom(ctx)
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       tc.SessionID,
		OwnerPersonID:   sessionFacts.OwnerPersonID,
		Posture:         sessionFacts.Posture,
		ProjectID:       tc.ProjectID,
		Agent:           tc.Agent,
		ParentSessionID: tc.ParentSessionID,
		ToolCallID:      tc.ToolCallID,
		ProjectDir:      tc.ActiveRootPath(),
	})
	if reject := e.validateInvocation(ctx, qualifiedName, profileID, args, &tc); reject != nil {
		return "", e.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, reject)
	}
	secretUse, parseErr := parseSecretUse(tc.Invocation.Contract, args)
	if parseErr != nil {
		return "", e.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"tool": qualifiedName, "reason": parseErr.Error()}))
	}
	ctx = withSecretUse(ctx, secretUse)
	args, tc, err = e.applyPreInvokeBoundary(ctx, qualifiedName, profileID, args, tc)
	if err != nil {
		return "", err
	}
	canonicalArgs := args
	// Handlers execute against resolved arguments and echo against these.
	tc.CanonicalArgs = canonicalArgs
	ctx = secretcap.WithResolution(ctx, tc.Secrets)
	executionArgs := tc.Secrets.Arguments
	if err := e.screenArgvSecrets(ctx, qualifiedName, executionArgs, tc); err != nil {
		return "", err
	}
	if err := e.screenFileSecrets(ctx, qualifiedName, executionArgs, tc); err != nil {
		return "", err
	}
	// The screened save is a file tool's transport.
	if tc.Invocation.Contract.SecretReferenceSurface.IsFile() {
		if err := tc.Secrets.HandOff(ctx, nil); err != nil {
			return "", e.rejectBeforeInvoke(ctx, qualifiedName, profileID, args, HeldHandOffReject(string(secretmatch.SurfaceFile)))
		}
	}
	tc.ProcessReview = e.processReviewer(qualifiedName, canonicalArgs, tc)
	tc.FileChangeReview = e.fileChangeReviewer(qualifiedName, canonicalArgs, tc)
	if e.blockPlane != nil {
		if err := e.blockPlane.Evaluate(ctx, oar.AnchorToolPreInvoke, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return "", err
		}
		if err := e.blockPlane.Evaluate(ctx, oar.AnchorToolHandler, qualifiedName, profileID, canonicalArgs, nil); err != nil {
			return "", err
		}
	}
	out, err = e.registry.Run(ctx, qualifiedName, executionArgs, tc)
	// A consumer may echo what it received; results carry references, never resolved values.
	out = tc.Secrets.ReferenceEchoes(out)
	err = referenceEchoesInError(tc.Secrets, err)
	if errors.Is(err, context.Canceled) {
		return out, err
	}
	capabilityDenied := e.takeCapabilityDenial(tc.SessionID, tc.ToolCallID)

	if err != nil {
		if capabilityDenied && AsToolReject(err) == nil {
			return out, fmt.Errorf("%w\n\n%s", err, e.outcomeMessage(approvaloutcome.CodeApprovalDenied, nil))
		}
		if host := HostRefusal(err); host != nil {
			return out, host
		}
		displayCmd := commandsurface.PrimaryCommandLine(canonicalArgs, nil)
		if tr := AsToolReject(err); tr != nil {
			return out, e.settleReject(ctx, qualifiedName, profileID, canonicalArgs, tr)
		}
		owner := tc.Invocation.Contract.Owner
		if tr := GitFailureObservation(err); tr != nil {
			return "", e.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := ArgvShapeObservation(qualifiedName, err); tr != nil {
			return "", e.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := CommandSurfaceObservation(qualifiedName, profileID, displayCmd, canonicalArgs,
			toolschema.ArgFieldPaths(e.argsSchemaFor(ctx, tc.SessionID, qualifiedName)), err); tr != nil {
			return "", e.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		if tr := ScopeObservation(profileID, tc.TurnSurfaceID, qualifiedName, pathFromToolArgs(canonicalArgs), err); tr != nil {
			return "", e.settleReject(ctx, qualifiedName, profileID, canonicalArgs,
				CompleteFailureMetadata(tr, qualifiedName, owner))
		}
		ownerRef := tc.Invocation.Contract.Owner
		if tc.Out != nil && strings.TrimSpace(tc.Out.OwnerRef) != "" {
			ownerRef = tc.Out.OwnerRef
		}
		tr := ownerFailure(qualifiedName, ownerRef, err)
		if e.blockPlane != nil {
			if ferr := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, canonicalArgs, tr); ferr != nil {
				return out, ferr
			}
		}
		// Subsystem-owner output survives this failure.
		return out, e.renderReject(tr)
	}
	return out, nil
}

func (e *DefaultToolExecutor) validateInvocation(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	tc *ToolContext,
) *ToolReject {
	if reject := ValidateCallArguments(qualifiedName, args, e.argsSchemaFor(ctx, tc.SessionID, qualifiedName), *tc); reject != nil {
		return reject
	}
	if reject := e.exactCommandReplacementReject(ctx, qualifiedName, profileID, args, *tc); reject != nil {
		return reject
	}
	if reject := argvShapeReject(qualifiedName, args); reject != nil {
		return reject
	}
	// Freeze the registry contract when the caller did not lease one.
	// Dynamic tools are absent from the compiled catalog.
	if strings.TrimSpace(tc.Invocation.Contract.Owner) == "" {
		def, ok := e.definitionFor(qualifiedName)
		if !ok {
			return &ToolReject{
				Code:         ToolOwnerFailedCode,
				FailureClass: api.FailureClassOwnerError,
				Data: map[string]any{
					"tool":   qualifiedName,
					"reason": "no registered definition to freeze for dispatch",
				},
			}
		}
		tc.Invocation.Contract = def.Contract
	}
	return ValidateCapabilityContract(tc.Invocation.Contract, args)
}

func (e *DefaultToolExecutor) exactCommandReplacementReject(
	ctx context.Context,
	toolName, profileID string,
	args map[string]any,
	tc ToolContext,
) *ToolReject {
	if !isCommandEquivalenceRunner(toolName) {
		return nil
	}
	schema := e.argsSchemaFor(ctx, tc.SessionID, toolName)
	envelope, ok := parseCommandEnvelope(commandArgsWithoutDefaults(args, schema))
	if !ok {
		return nil
	}
	calls, ok := envelope.replacements(ctx, tc.ActiveRootPath(), tc.SessionScratchDir)
	if !ok {
		return nil
	}
	for _, call := range calls {
		if _, callable := e.definitionFor(call.Tool); !callable {
			return nil
		}
		decision, _ := EvaluateListVisible(ctx, e.policy, platform.PolicyContext{
			ProfileID: profileID, ToolAccess: tc.ToolAccess, ToolName: call.Tool,
		})
		if decision == nil || !decision.Allowed {
			return nil
		}
		schema := e.argsSchemaFor(ctx, tc.SessionID, call.Tool)
		if schema == nil {
			return nil
		}
		argsValid := ValidateToolArgs(schema, call.Args) == nil
		if !argsValid {
			return nil
		}
	}
	return exactReplacementReject(envelope.command, profileID, calls)
}

// definitionFor reads the registry snapshot dispatch will run.
func (e *DefaultToolExecutor) definitionFor(qualifiedName string) (Definition, bool) {
	if e == nil || e.registry == nil {
		return Definition{}, false
	}
	return e.registry.Definition(qualifiedName)
}

func (e *DefaultToolExecutor) prepareWorkerBranch(ctx context.Context, tool, profileID string, args map[string]any, tc ToolContext) (ToolContext, error) {
	prepared, err := e.ensureWorkerBranchIfNeeded(ctx, tool, tc)
	if err == nil {
		return prepared, nil
	}
	reject := AsToolReject(err)
	if reject == nil {
		reject = &ToolReject{Code: "WORKER_BRANCH_CLAIM_FAILED", Data: map[string]any{"tool": tool, "reason": err.Error()}}
	}
	reject = CompleteFailureMetadata(reject, tool, tc.Invocation.Contract.Owner)
	return tc, e.rejectBeforeInvoke(ctx, tool, profileID, args, reject)
}

func (e *DefaultToolExecutor) ensureWorkerBranchIfNeeded(ctx context.Context, tool string, tc ToolContext) (ToolContext, error) {
	if !RequiresWorkerBranch(tool) {
		return tc, nil
	}
	if strings.TrimSpace(tc.WorkerJobID) == "" || strings.TrimSpace(tc.WorkerBranchRoot) != "" {
		return tc, nil
	}
	if tc.WorkerCoord == nil {
		return tc, &ToolReject{
			Code: "WORKER_BRANCH_CLAIM_FAILED",
			Data: map[string]any{"tool": tool, "reason": "worker branch coordinator not configured"},
		}
	}
	return tc.WorkerCoord.EnsureWorkerBranch(ctx, tc)
}
