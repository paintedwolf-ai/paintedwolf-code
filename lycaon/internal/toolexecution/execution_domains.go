package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolapproval"

	"github.com/lycaon/lycaon/internal/toolfeedback"

	"context"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"sync"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/destconfig"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/pkgregistry"
)

// Executor owns tool invocation dispatch.
type Executor struct {
	Approvals      *Approvals
	Boundary       *Boundary
	Capabilities   *Capabilities
	Metadata       *Metadata
	Network        *Network
	Rejections     *Rejections
	Secrets        *Secrets
	defaultProfile string
}

// Metadata owns tool definitions and schema presentation.
type Metadata struct {
	Rejections   *Rejections
	policy       platform.PolicyEngine
	registry     *tools.DefaultRegistry
	toolSchemas  *toolschema.Config
	schemaSource func(ctx context.Context, sessionID string) *toolschema.Config
}

// Approvals owns approval coordination and checkpoint lifetime.
type Approvals struct {
	Metadata          *Metadata
	Network           *Network
	Rejections        *Rejections
	Secrets           *Secrets
	checkpointMgr     hitl.CheckpointManager
	approvalGate      hitl.ApprovalGate
	approvalExplainer ApprovalExplainer
	approvalOutcome   ApprovalOutcomeRenderer
	aiRationale       toolapproval.AIRationaleAttacher
	authzRecorder     authzledger.Recorder
	approvalCoalesce  toolapproval.ToolApprovalCoalesce
	gateRepeat        toolapproval.GateRepeatLedger
	consequence       toolapproval.ConsequenceDeriver
	backgroundCommand BackgroundCommandResolver
}

// Network owns endpoint discovery and egress fan-in.
type Network struct {
	Approvals          *Approvals
	Boundary           *Boundary
	Rejections         *Rejections
	Secrets            *Secrets
	egressPostureFor   func(projectDir string) gate.Posture
	hostResourceSource HostResourceConnectionSource
	hostLedger         SessionHostLedger
	egressGather       *egressGathers
	egressGatherOnce   sync.Once
	destinations       *destconfig.Registry
	registries         *pkgregistry.Catalog
}

// Capabilities owns pre-spawn network and process authority.
type Capabilities struct {
	Approvals           *Approvals
	Boundary            *Boundary
	Rejections          *Rejections
	Secrets             *Secrets
	socketRuntime       tools.SocketCapabilityRuntime
	durableSockets      tools.DurableSocketSource
	directIPRuntime     tools.DirectIPCapabilityRuntime
	directIPLifecycle   tools.DirectIPLifecycleHook
	approvalsDisabled   func(projectDir string) bool
	localListenGate     tools.LocalListenGate
	loopbackConnectGate tools.LoopbackConnectGate
	localNetworkGate    tools.LocalNetworkGate
}

// Boundary owns the confinement request and path review.
type Boundary struct {
	Approvals            *Approvals
	Metadata             *Metadata
	Rejections           *Rejections
	sessionOverlay       SessionWriteRootOverlay
	sessionListenGrant   tools.SessionListenGrant
	sessionLoopbackGrant tools.SessionLoopbackGrant
	writeRootPreflight   WriteRootPreflight
	readPathPreflight    ReadPathPreflight
	packageExecution     *packageexec.Service
	packageExecutionErr  error
	sessionReadOverlay   SessionReadPathOverlay
}

// Secrets owns screening, resolution and held-value release.
type Secrets struct {
	Rejections          *Rejections
	Approvals           *Approvals
	Boundary            *Boundary
	Capabilities        *Capabilities
	Network             *Network
	presenceAvailableFn func() bool
	vaultUnlocks        *presence.Unlocks
	heldAsks            heldAskCounter
	secretMatcher       *secretmatch.Matcher
	secretIgnores       *projectignore.SecretService
	secretResolver      func(context.Context, map[string]any, secretcap.ResolveContext) (*secretcap.Resolution, error)
	secretReceiptOnce   sync.Once
	secretReceiptRT     *secretReceiptRuntime
	secretExposure      func(ctx context.Context, chatSessionID string) (bool, error)
	untrustedIngestion  func(ctx context.Context, chatSessionID string) (bool, error)
}

// Rejections owns structured refusal presentation.
type Rejections struct {
	blockPlane        *toolfeedback.BlockPlane
	capabilityDenials sync.Map
	rejectFmt         *guidance.StaticRejectFormatter
}

func NewExecutor(policy platform.PolicyEngine, registry *tools.DefaultRegistry, defaultProfile string) *Executor {
	if defaultProfile == "" {
		defaultProfile = toolprofiles.DefaultToolProfileID
	}
	executor := &Executor{}
	metadata := &Metadata{}
	approvals := &Approvals{}
	network := &Network{}
	capabilities := &Capabilities{}
	boundary := &Boundary{}
	secrets := &Secrets{}
	rejections := &Rejections{}
	executor.defaultProfile = defaultProfile
	metadata.policy = policy
	metadata.registry = registry
	boundary.packageExecution, boundary.packageExecutionErr = packageexec.NewService()
	executor.Metadata = metadata
	executor.Secrets = secrets
	executor.Rejections = rejections
	executor.Network = network
	executor.Boundary = boundary
	executor.Capabilities = capabilities
	executor.Approvals = approvals
	metadata.Rejections = rejections
	approvals.Network = network
	approvals.Metadata = metadata
	approvals.Secrets = secrets
	approvals.Rejections = rejections
	network.Boundary = boundary
	network.Secrets = secrets
	network.Rejections = rejections
	network.Approvals = approvals
	capabilities.Boundary = boundary
	capabilities.Secrets = secrets
	capabilities.Rejections = rejections
	capabilities.Approvals = approvals
	boundary.Metadata = metadata
	boundary.Rejections = rejections
	boundary.Approvals = approvals
	secrets.Rejections = rejections
	secrets.Network = network
	secrets.Boundary = boundary
	secrets.Capabilities = capabilities
	secrets.Approvals = approvals
	return executor
}
