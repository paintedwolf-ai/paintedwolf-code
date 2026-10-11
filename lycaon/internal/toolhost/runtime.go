package toolhost

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/fileage"
)

// Runtime composes native services with tool dispatch and confinement.
type Runtime struct {
	Boundary  *sandbox.Boundary
	Registry  *tools.DefaultRegistry
	Executor  *toolexecution.Executor
	Skills    *SkillServices
	Survey    *SurveyServices
	Commands  *CommandServices
	Mutations *MutationServices
	Web       *WebServices
	Authority *AuthorityServices
}

// RuntimeConfig holds config file paths relative to the module root.
type RuntimeConfig struct {
	ConfigRoot string
	Approvals  *settings.ApprovalStore
	Catalog    *extpacks.EffectiveCatalog
	// Activation is the loaded-schema set shared with the session turn ledger;
	// an isolated runtime keeps its own in-memory set.
	Activation tools.SchemaActivation
	// RequestResolver ranks loadable schemas against request_tools text; nil
	// resolves by exact names and word overlap alone.
	RequestResolver tools.RequestResolver
	RequestObserver tools.RequestObserver
	// SkillLookup ranks loaded skills against skills_read text; nil matches
	// by word overlap alone.
	SkillLookup tools.SkillLookup
	// Rerank blends the decision engine into task-ranked tool results.
	Rerank decide.Reranker
}

// NewRuntime loads sandbox, profiles, native tools, and constructs the executor chain.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	sandboxCfg, err := sandbox.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("sandbox config: %w", err)
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return nil, fmt.Errorf("tool profiles: %w", err)
	}
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)
	if scopes, err := sandbox.LoadPathScopes(); err != nil {
		return nil, fmt.Errorf("path scopes: %w", err)
	} else {
		boundary.SetPathScopes(scopes)
	}

	nativeCfg, err := nativemanifest.Load()
	if err != nil {
		return nil, fmt.Errorf("native tools config: %w", err)
	}
	commandRunner := hostcmd.NewRunner()

	gitMgr := git.NewManager()
	statusCache := git.NewStatusCache(gitMgr)
	surveyCat, err := survey.LoadCatalog(survey.CatalogDir())
	if err != nil {
		return nil, fmt.Errorf("survey catalog: %w", err)
	}
	ageProvider := fileage.New(gitMgr)
	schemaCfg, _, err := extpacks.LoadEffectiveToolSchemas(cfg.Catalog)
	if err != nil {
		return nil, fmt.Errorf("tool schemas: %w", err)
	}
	activation := cfg.Activation
	if activation == nil {
		activation = tools.NewMemoryActivation()
	}
	statusPointer := newStatusCacheBinding(statusCache)
	runtime := &Runtime{Boundary: boundary, Web: &WebServices{boundary: boundary}, Authority: &AuthorityServices{}}
	registry, mutationTools, err := buildNativeRegistry(buildDeps{
		boundary:      boundary,
		git:           gitMgr,
		statusCache:   statusPointer,
		command:       commandRunner,
		nativeConfig:  nativeCfg,
		toolSchemas:   schemaCfg,
		surveyCatalog: surveyCat,
		fileAge:       ageProvider,
		rerank:        cfg.Rerank,
	})
	if err != nil {
		return nil, err
	}
	runtime.Registry = registry
	runtime.Survey = mutationTools.Survey
	runtime.Mutations = mutationTools.Mutations
	runtime.Commands = mutationTools.Commands
	runtime.Skills = mutationTools.Skills
	runtime.Skills.skillsReadTool.Lookup = cfg.SkillLookup
	if nativeCfg.HasTool("request_tools") {
		if err := tools.RegisterRequestTools(registry, tools.RequestToolsDeps{
			Activation: activation,
			Boundary:   boundary,
			Resolve:    cfg.RequestResolver,
			Record:     cfg.RequestObserver,
			RejectFmt:  func() *guidance.StaticRejectFormatter { return runtime.Authority.rejectFmt },
		}); err != nil {
			return nil, fmt.Errorf("request_tools: %w", err)
		}
	}

	profilePolicy := toolprofiles.NewProfilePolicyEngine(boundary)
	runtime.Authority.profilePolicy = profilePolicy
	// The deferred handle remains closed until every producer is registered.
	var approvalGate hitl.ApprovalGate
	switch {
	case confine.BypassEnabled():
		// Bypass mode disables approvals and confinement.
		approvalGate = settings.NewBypassApprovalGate()
	case cfg.Approvals != nil:
		// Each action carries its applied confinement facts.
		builder, handle := settings.NewGateBuilder(cfg.Approvals)
		runtime.Authority.gateBuilder = builder
		runtime.Authority.gateHandle = handle
		approvalGate = handle
	}
	policy := toolexecution.NewApprovalPolicyEngine(profilePolicy, approvalGate)
	executor := toolexecution.NewExecutor(policy, registry, toolprofiles.DefaultToolProfileID)

	runtime.Executor = executor
	runtime.Authority.reviews = executor.Approvals
	runtime.Authority.paths = executor.Boundary
	runtime.Authority.capabilities = executor.Capabilities
	runtime.Authority.metadata = executor.Metadata
	runtime.Authority.rejections = executor.Rejections
	runtime.Commands.reviews = executor.Approvals
	runtime.Commands.paths = executor.Boundary
	runtime.Web.policy = profilePolicy

	runtime.Authority.approvalGate = approvalGate
	runtime.Authority.approvals = cfg.Approvals
	executor.Network.SetEgressPostureSource(runtime.Authority.ApprovalPosture)
	reg, err := approvals.LoadRegistryStock()
	if err != nil {
		return nil, fmt.Errorf("approval explanation catalog: %w", err)
	}
	runtime.Executor.Approvals.SetApprovalExplainer(newRegistryExplainer(reg))
	outcomeCfg, err := approvaloutcome.Load()
	if err != nil {
		return nil, fmt.Errorf("approval outcome catalog: %w", err)
	}
	runtime.Executor.Approvals.SetApprovalOutcomeRenderer(newCatalogOutcomeRenderer(approvaloutcome.NewCatalog(outcomeCfg)))
	if cfg.Approvals != nil {
		wireEgressPolicy(runtime.Authority, cfg.Approvals)
	}
	return runtime, nil
}

// wireEgressPolicy applies live approval policy to egress checks.
