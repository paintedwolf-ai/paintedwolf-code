package toolhost

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativejq "github.com/lycaon/lycaon/internal/tools/native/jq"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// Runtime bundles the wired native tool stack for the host process.
type Runtime struct {
	Boundary *sandbox.Boundary
	Registry *tools.DefaultRegistry
	Executor *tools.DefaultToolExecutor
	// activation is the per-session loaded-schema set request_tools writes.
	activation         tools.SchemaActivation
	readTool           *surveytools.ReadTool
	listDirTool        *surveytools.ListDirTool
	grepTool           *surveytools.GrepTool
	findTool           *surveytools.FindTool
	summarizeTool      *surveytools.SummarizeTool
	writeTool          *native.WriteTool
	editTool           *native.EditTool
	replaceLinesTool   *native.ReplaceLinesTool
	codeRewriteTool    *native.CodeRewriteTool
	restoreVersionTool *native.RestoreVersionTool
	jqEditTool         *native.JqEditTool
	approvalGate       hitl.ApprovalGate
	approvalRules      settings.ApprovalRuleCatalogSource
	authzRecorder      authzledger.Recorder
	// gateBuilder collects producers before gateHandle is sealed.
	gateBuilder *settings.GateBuilder
	gateHandle  *settings.DeferredGate
	// approvals supplies live posture to egress checks.
	approvals               *settings.ApprovalStore
	checkpointMgr           hitl.CheckpointManager
	rejectFmt               *guidance.StaticRejectFormatter
	approvalExplainer       tools.ApprovalExplainer
	approvalOutcome         tools.ApprovalOutcomeRenderer
	aiRationale             tools.AIRationaleAttacher
	directDiscovererFactory webresearch.DirectDiscovererFactory
	profilePolicy           *tools.ProfilePolicyEngine
	bgRegistry              *bgprocess.Registry
	commandTool             *native.CommandTool
	verifyTool              *native.VerifyTool
	commandOutputTool       *native.CommandOutputTool
	commandStopTool         *native.CommandStopTool
	skillsReadTool          *skilltools.SkillsReadTool
	fileAge                 *fileage.Provider
	// gitStatusCache is shared with the board GET path when the host wires it.
	// Native git_status always loads with force=true.
	gitStatusCache atomic.Pointer[git.StatusCache]
	// releaseOwnStatusCache unbinds the default cache from repochange once a
	// host cache replaces it; repeated calls are no-ops.
	releaseOwnStatusCache func()
}

// RenderSkillBody uses the same renderer as skills_read.
func (r *Runtime) RenderSkillBody(ctx context.Context, tctx tools.ToolContext, sk skills.Skill) (string, error) {
	if r == nil || r.skillsReadTool == nil {
		return "", fmt.Errorf("skills tool not wired")
	}
	return r.skillsReadTool.RenderBody(ctx, tctx, sk)
}

// SetSkillsCatalog wires the session-resolved skill catalog into skills_read.
func (r *Runtime) SetSkillsCatalog(fn func(ctx context.Context, tctx tools.ToolContext) []skills.Skill) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.Skills = fn
}

// SetSkillTemplateVars supplies pongo context for pack skill bodies at skills_read.
func (r *Runtime) SetSkillTemplateVars(fn func(ctx context.Context, tctx tools.ToolContext) map[string]any) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.TemplateVars = fn
}

// SetSkillPackConfiguration supplies a pack's own configuration values to its
// skill bodies at skills_read.
func (r *Runtime) SetSkillPackConfiguration(
	fn func(ctx context.Context, tctx tools.ToolContext, packID string) map[string]any,
) {
	if r == nil || r.skillsReadTool == nil {
		return
	}
	r.skillsReadTool.PackConfiguration = fn
}

// WarmFileAge starts a shared background build for the repository.
func (r *Runtime) WarmFileAge(ctx context.Context, projectDir string) {
	if r == nil {
		return
	}
	r.fileAge.Warm(ctx, projectDir)
}

// InvalidateFileAge expires cached ages when HEAD moves.
func (r *Runtime) InvalidateFileAge(projectDir string) {
	if r == nil {
		return
	}
	r.fileAge.Invalidate(projectDir)
}

// SetGitStatusCache shares the host StatusCache with native git_status (force=true)
// so board Invalidate and the Git status read use one map.
func (r *Runtime) SetGitStatusCache(cache *git.StatusCache) {
	if r == nil {
		return
	}
	if previous := r.gitStatusCache.Swap(cache); previous != cache && r.releaseOwnStatusCache != nil {
		r.releaseOwnStatusCache()
	}
}

// SetBackgroundRegistry wires session-scoped background command processes.
func (r *Runtime) SetBackgroundRegistry(reg *bgprocess.Registry) {
	if r == nil {
		return
	}
	r.bgRegistry = reg
	if r.commandTool != nil {
		r.commandTool.Background = reg
	}
	if r.verifyTool != nil {
		r.verifyTool.Background = reg
	}
	if r.commandOutputTool != nil {
		r.commandOutputTool.Registry = reg
	}
	if r.commandStopTool != nil {
		r.commandStopTool.Registry = reg
	}
	if r.Executor != nil {
		r.Executor.SetBackgroundCommandResolver(r.backgroundCommandLine)
	}
}

// SetReadEvidenceLedger wires session evidence for read-time reference receipts.
func (r *Runtime) SetReadEvidenceLedger(ledger guidance.EvidenceLedgerReader) {
	if r == nil || r.readTool == nil {
		return
	}
	r.readTool.Ledger = ledger
}

// SetApprovalRuleSource sets extension policy for tool and egress checks.
func (r *Runtime) SetApprovalRuleSource(src settings.ApprovalRuleCatalogSource) {
	if r == nil || src == nil {
		return
	}
	r.approvalRules = src
	if r.gateBuilder != nil {
		r.gateBuilder.WithApprovalRules(src)
	}
}

// SealApprovalGate installs the gate built from all registered producers.
func (r *Runtime) SealApprovalGate() {
	if r == nil || r.gateBuilder == nil {
		return
	}
	r.gateBuilder.Seal()
}

// ApprovalGateSealed reports whether the real gate is installed.
func (r *Runtime) ApprovalGateSealed() bool {
	return r != nil && (r.gateBuilder == nil || r.gateHandle.Sealed())
}

// SetEgressDetectionSource wires CONNECT-hold detection matching into confine.
func (r *Runtime) SetEgressDetectionSource(src confine.EgressDetectionSource) {
	if r == nil {
		return
	}
	confine.SetEgressDetectionSource(src)
	// Wired with or without a store: a nil reader hands the seam an empty posture.
	store := r.approvals
	confine.SetDetectionApprovalPosture(func(cmd confine.EgressCommand) gate.Posture {
		return effectiveEgressApprovalConfig(store, cmd).Posture
	})
}

func effectiveEgressApprovalConfig(store *settings.ApprovalStore, cmd confine.EgressCommand) settings.ApprovalConfig {
	if store == nil {
		return settings.ApprovalConfig{Posture: gate.DefaultPosture}
	}
	if projectDir := filepath.Clean(cmd.ProjectDir); cmd.ProjectDir != "" && projectDir != "." {
		return store.Get(llm.SettingsScopeProject, settings.ProjectRef{ID: cmd.ProjectID, Dir: projectDir})
	}
	return store.Get(llm.SettingsScopeGlobal, settings.ProjectRef{})
}

// ApprovalPosture reports the effective ask-line for an attributed project.
func (r *Runtime) ApprovalPosture(projectDir string) gate.Posture {
	if r == nil || r.approvals == nil {
		return gate.DefaultPosture
	}
	return effectiveEgressApprovalConfig(r.approvals, confine.EgressCommand{ProjectDir: projectDir}).Posture
}

// ApprovalsDisabled reports the effective never-ask override for an attributed project.
// A tighter project overlay may restore asking even when the device-level switch is on.
func (r *Runtime) ApprovalsDisabled(projectDir string) bool {
	if r == nil || r.approvals == nil {
		return false
	}
	cfg := effectiveEgressApprovalConfig(r.approvals, confine.EgressCommand{ProjectDir: projectDir})
	return cfg.NeverAsk != nil && *cfg.NeverAsk
}

// WriteRootRule reports the effective rule for one proposed root.
func (r *Runtime) WriteRootRule(ctx context.Context, projectID, projectDir, root string) (settings.ApprovalRule, bool) {
	if r == nil || r.approvals == nil {
		return settings.ApprovalRule{}, false
	}
	scope := llm.SettingsScopeGlobal
	ref := settings.ProjectRef{ID: projectID, Dir: projectDir}
	if projectID != "" || projectDir != "" {
		scope = llm.SettingsScopeProject
	}
	layers := settings.EffectiveApprovalRuleLayers(ctx, r.approvals, r.approvalRules, scope, ref)
	return settings.EvaluateWriteRootRuleLayers(layers, root)
}

// Changed remote tool definitions require renewed approval.
func (r *Runtime) SetMCPToolPinSource(src settings.MCPToolPinSource) {
	if r == nil || r.gateBuilder == nil {
		return
	}
	r.gateBuilder.WithPins(src)
}

// SetListDirUnionBrief wires multi-root orientation briefs for unbounded list_dir.
func (r *Runtime) SetListDirUnionBrief(fn repomap.UnionOrientationBrief) {
	if r == nil || r.listDirTool == nil {
		return
	}
	r.listDirTool.UnionBrief = fn
}

// SetScopeGuards wires file-count thresholds and catalog counts.
func (r *Runtime) SetScopeGuards(cfg toolscope.Config, fileCount func(projectDir string) (int, bool)) {
	if r == nil {
		return
	}
	toolscope.SetGlobal(cfg)
	cfgCopy := cfg
	if r.grepTool != nil {
		r.grepTool.Scope = &cfgCopy
		r.grepTool.FileCount = fileCount
	}

}

// SetDirectDiscovererFactory wires the bundled direct-search factory for web_search.
func (r *Runtime) SetDirectDiscovererFactory(factory webresearch.DirectDiscovererFactory) {
	if r == nil {
		return
	}
	r.directDiscovererFactory = factory
}

// SetWebResearchConfig wires search_enabled prefs into tool listing and invoke policy.
func (r *Runtime) SetWebResearchConfig(cfg *webresearch.ConfigStore) {
	if r == nil {
		return
	}
	if r.profilePolicy == nil {
		r.profilePolicy = tools.NewProfilePolicyEngine(r.Boundary)
	}
	r.profilePolicy.SetRuntimeToolDeny(webresearch.SearchToolRuntimeDeny(cfg))
}

// DirectFactoryGetter returns a getter for the registered direct discoverer factory.
func (r *Runtime) DirectFactoryGetter() webresearch.FactoryGetter {
	if r == nil || r.directDiscovererFactory == nil {
		return nil
	}
	factory := r.directDiscovererFactory
	return func() webresearch.DirectDiscovererFactory { return factory }
}

// SetContentApply wires edit review before filesystem writes.
func (r *Runtime) SetContentApply(contentApply native.ContentApplyGate) {
	if r == nil {
		return
	}
	if r.writeTool != nil {
		r.writeTool.ContentApply = contentApply
	}
	if r.editTool != nil {
		r.editTool.ContentApply = contentApply
	}
	if r.replaceLinesTool != nil {
		r.replaceLinesTool.ContentApply = contentApply
	}
	if r.codeRewriteTool != nil {
		r.codeRewriteTool.ContentApply = contentApply
	}
	if r.restoreVersionTool != nil {
		r.restoreVersionTool.ContentApply = contentApply
	}
	if r.jqEditTool != nil {
		r.jqEditTool.ContentApply = contentApply
	}
}

// SetBlueprintWriteObserver wires host advance after bound blueprint file writes.
func (r *Runtime) SetBlueprintWriteObserver(o native.BlueprintWriteObserver) {
	if r == nil {
		return
	}
	native.SetBlueprintWriteObserver(o)
}

// SetApprovalExplainer wires reviewed explanation copy on tool_approval checkpoints.
func (r *Runtime) SetApprovalExplainer(explainer tools.ApprovalExplainer) {
	if r == nil {
		return
	}
	r.approvalExplainer = explainer
	if r.Executor != nil {
		r.Executor.SetApprovalExplainer(explainer)
	}
}

// SetApprovalOutcomeRenderer wires agent-facing copy for denied/expired checkpoints.
func (r *Runtime) SetApprovalOutcomeRenderer(renderer tools.ApprovalOutcomeRenderer) {
	if r == nil {
		return
	}
	r.approvalOutcome = renderer
	if r.Executor != nil {
		r.Executor.SetApprovalOutcomeRenderer(renderer)
	}
}

// SetAuthzRecorder wires tamper-evident tool_denied recording on the executor.
func (r *Runtime) SetAuthzRecorder(rec authzledger.Recorder) {
	if r == nil || r.Executor == nil {
		return
	}
	r.authzRecorder = rec
	r.Executor.SetAuthzRecorder(rec)
}

// ReleaseSessionRun drops cached egress verdicts; approved authority stays.
func (r *Runtime) ReleaseSessionRun(sessionID string) {
	if r == nil {
		return
	}
	confine.ForgetEgressSession(sessionID)
}

// ForgetSessionAuthorization releases a disposed chat's approvals and quiets.
func (r *Runtime) ForgetSessionAuthorization(sessionID string) {
	if r == nil {
		return
	}
	if r.approvalGate != nil {
		r.approvalGate.ForgetSession(sessionID)
	}
}

// SetCheckpointManager wires tool approval suspend/resume on the executor.
func (r *Runtime) SetCheckpointManager(mgr hitl.CheckpointManager) {
	if r == nil {
		return
	}
	r.checkpointMgr = mgr
	if r.Executor != nil {
		r.Executor.SetCheckpointManager(mgr, r.approvalGate)
	}
}

// ApprovalGate returns the host approval authority for API and checkpoint brokers.
func (r *Runtime) ApprovalGate() hitl.ApprovalGate {
	if r == nil {
		return nil
	}
	return r.approvalGate
}

// SetSandboxWriteRootGate installs write-root review and grants.
func (r *Runtime) SetSandboxWriteRootGate(writeRoot native.SandboxWriteRootGate) {
	if r == nil {
		return
	}
	if r.commandTool != nil {
		r.commandTool.WriteRootGate = writeRoot
	}
	if r.verifyTool != nil {
		r.verifyTool.WriteRootGate = writeRoot
	}
	if r.Executor != nil {
		r.Executor.SetSessionWriteRootOverlay(writeRoot.SessionWriteRoots)
		r.Executor.SetWriteRootPreflight(func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, root string) (bool, bool, string, error) {
			result, err := writeRoot.Authorize(ctx, native.SandboxWriteRootAsk{
				SessionID: tc.SessionID, ParentSessionID: tc.ParentSessionID,
				ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedWriteRoot: root,
				SessionScratchRoot: tc.SessionScratchDir,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
		r.Executor.SetSessionReadPathOverlay(writeRoot.SessionReadPaths)
		r.Executor.SetReadPathPreflight(func(ctx context.Context, tool string, args map[string]any, tc tools.ToolContext, path string) (bool, bool, string, error) {
			result, err := writeRoot.AuthorizeRead(ctx, native.SandboxReadPathAsk{
				SessionID: tc.SessionID, ParentSessionID: tc.ParentSessionID,
				ProjectID: tc.ProjectID, ToolCallID: tc.ToolCallID, ProjectDir: tc.ActiveRootPath(),
				ToolName: tool, Command: commandsurface.PrimaryCommandLine(args, nil), ProposedReadPath: path,
				ReadDenyPaths: tools.ActionConfineInputsForContext(tc, nil).ReadDenyPaths,
			})
			return result.Authorized, result.Denied, result.UserGuidance, err
		})
	}
}

// SetVerifyDeclaredCommand shares project check identity across execution tools.
func (r *Runtime) SetVerifyDeclaredCommand(resolve native.DeclaredVerifyCommand) {
	if r == nil {
		return
	}
	if r.verifyTool != nil {
		r.verifyTool.DeclaredCommand = resolve
	}
	if r.commandTool != nil {
		r.commandTool.DeclaredCommand = resolve
	}
}

// SetSandboxListenGate wires the local-listener raise/await and its chat lease
// onto every tool that runs agent-chosen argv under the box.
func (r *Runtime) SetSandboxListenGate(listen tools.LocalListenGate) {
	if r == nil {
		return
	}
	if r.Executor != nil {
		r.Executor.SetSessionListenGrant(listen.SessionListenGrant)
		r.Executor.SetLocalListenGate(listen)
	}
}

// SetSandboxLoopbackGate wires local TCP/UDP client asks and their chat lease
// onto every tool that runs agent-chosen argv under confinement.
func (r *Runtime) SetSandboxLoopbackGate(loopback tools.LoopbackConnectGate) {
	if r == nil {
		return
	}
	if r.Executor != nil {
		r.Executor.SetSessionLoopbackGrant(loopback.SessionLoopbackGrant)
		r.Executor.SetLoopbackConnectGate(loopback)
	}
}

// SetLocalNetworkGate wires the combined listen+connect preflight card.
func (r *Runtime) SetLocalNetworkGate(localNetwork tools.LocalNetworkGate) {
	if r == nil || r.Executor == nil {
		return
	}
	r.Executor.SetLocalNetworkGate(localNetwork)
}

// SetToolApprovalCoalesce wires mint/join spam guards onto the tool executor.
func (r *Runtime) SetToolApprovalCoalesce(rt *approvalstate.ToolApprovalCoalesce) {
	if r == nil || r.Executor == nil || rt == nil {
		return
	}
	r.Executor.SetToolApprovalCoalesce(toolApprovalCoalesceAdapter{rt: rt})
}

// SetGateRepeatLedger wires reason-keyed repeat counting onto the tool executor.
func (r *Runtime) SetGateRepeatLedger(rt *approvalstate.GateRepeatLedger) {
	if r == nil || r.Executor == nil || rt == nil {
		return
	}
	r.Executor.SetGateRepeatLedger(gateRepeatLedgerAdapter{rt: rt})
}

// SetAIRationaleAttacher wires host lite-model AI rationale patching on
// tool_approval checkpoints (awaitApproval path only).
func (r *Runtime) SetAIRationaleAttacher(a tools.AIRationaleAttacher) {
	if r == nil {
		return
	}
	r.aiRationale = a
	if r.Executor != nil {
		r.Executor.SetAIRationaleAttacher(a)
	}
}

// ApplyGuidanceRejects wraps policy with OAR-routed observation rejects.
func (r *Runtime) ApplyGuidanceRejects(formatter *guidance.StaticRejectFormatter) {
	if r == nil || r.Executor == nil || formatter == nil {
		return
	}
	r.rejectFmt = formatter
	if r.profilePolicy == nil {
		r.profilePolicy = tools.NewProfilePolicyEngine(r.Boundary)
	}
	profilePolicy := r.profilePolicy
	approvalPolicy := tools.NewApprovalPolicyEngine(profilePolicy, r.approvalGate)
	guardPolicy := tools.NewGuidanceRejectPolicy(approvalPolicy)
	r.Executor = tools.NewDefaultToolExecutor(guardPolicy, r.Registry, tools.DefaultToolProfileID)
	r.Executor.SetRejectFormatter(formatter)
	if r.approvalExplainer != nil {
		r.Executor.SetApprovalExplainer(r.approvalExplainer)
	}
	if r.approvalOutcome != nil {
		r.Executor.SetApprovalOutcomeRenderer(r.approvalOutcome)
	}
	if r.checkpointMgr != nil {
		r.Executor.SetCheckpointManager(r.checkpointMgr, r.approvalGate)
	}
	if r.aiRationale != nil {
		r.Executor.SetAIRationaleAttacher(r.aiRationale)
	}
	if r.bgRegistry != nil {
		r.Executor.SetBackgroundCommandResolver(r.backgroundCommandLine)
	}
}

func (r *Runtime) backgroundCommandLine(sessionID, handle string) string {
	if r == nil || r.bgRegistry == nil {
		return ""
	}
	command, err := r.bgRegistry.CommandLine(sessionID, handle)
	if err != nil {
		return ""
	}
	return command
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
	releaseStatusCache := statusCache.RegisterRepochangeObserver()
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
	runtime := &Runtime{
		Boundary:   boundary,
		activation: activation,
		fileAge:    ageProvider,

		releaseOwnStatusCache: releaseStatusCache,
	}
	runtime.gitStatusCache.Store(statusCache)
	registry, mutationTools, err := buildNativeRegistry(buildDeps{
		boundary:      boundary,
		git:           gitMgr,
		statusCache:   &runtime.gitStatusCache,
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
	runtime.readTool = mutationTools.read
	runtime.listDirTool = mutationTools.listDir
	runtime.grepTool = mutationTools.grep
	runtime.findTool = mutationTools.find
	runtime.summarizeTool = mutationTools.summarize
	runtime.writeTool = mutationTools.write
	runtime.editTool = mutationTools.edit
	runtime.replaceLinesTool = mutationTools.replaceLines
	runtime.codeRewriteTool = mutationTools.codeRewrite
	runtime.restoreVersionTool = mutationTools.restoreVersion
	runtime.jqEditTool = mutationTools.jqEdit
	runtime.commandTool = mutationTools.commandTool
	runtime.verifyTool = mutationTools.verifyTool
	runtime.commandOutputTool = mutationTools.commandOutputTool
	runtime.commandStopTool = mutationTools.commandStopTool
	runtime.skillsReadTool = mutationTools.skillsRead
	runtime.skillsReadTool.Lookup = cfg.SkillLookup
	if nativeCfg.HasTool("request_tools") {
		if err := tools.RegisterRequestTools(registry, tools.RequestToolsDeps{
			Activation: runtime.activation,
			Boundary:   boundary,
			Resolve:    cfg.RequestResolver,
			Record:     cfg.RequestObserver,
			RejectFmt:  func() *guidance.StaticRejectFormatter { return runtime.rejectFmt },
		}); err != nil {
			return nil, fmt.Errorf("request_tools: %w", err)
		}
	}

	profilePolicy := tools.NewProfilePolicyEngine(boundary)
	runtime.profilePolicy = profilePolicy
	// The deferred handle remains closed until every producer is registered.
	var approvalGate hitl.ApprovalGate
	switch {
	case confine.BypassEnabled():
		// Bypass mode disables approvals and confinement.
		approvalGate = settings.NewBypassApprovalGate()
	case cfg.Approvals != nil:
		// Each action carries its applied confinement facts.
		builder, handle := settings.NewGateBuilder(cfg.Approvals)
		runtime.gateBuilder = builder
		runtime.gateHandle = handle
		approvalGate = handle
	}
	policy := tools.NewApprovalPolicyEngine(profilePolicy, approvalGate)
	executor := tools.NewDefaultToolExecutor(policy, registry, tools.DefaultToolProfileID)

	if cfg.Approvals != nil {
		wireEgressPolicy(runtime, cfg.Approvals)
	}

	runtime.Executor = executor
	runtime.approvalGate = approvalGate
	runtime.approvals = cfg.Approvals
	executor.SetEgressPostureSource(runtime.ApprovalPosture)
	reg, err := approvals.LoadRegistryStock()
	if err != nil {
		return nil, fmt.Errorf("approval explanation catalog: %w", err)
	}
	runtime.SetApprovalExplainer(newRegistryExplainer(reg))
	outcomeCfg, err := approvaloutcome.Load()
	if err != nil {
		return nil, fmt.Errorf("approval outcome catalog: %w", err)
	}
	runtime.SetApprovalOutcomeRenderer(newCatalogOutcomeRenderer(approvaloutcome.NewCatalog(outcomeCfg)))
	return runtime, nil
}

// wireEgressPolicy applies live approval policy to egress checks.
func wireEgressPolicy(runtime *Runtime, store *settings.ApprovalStore) {
	confine.SetEgressRuleEvaluator(func(ctx context.Context, cmd confine.EgressCommand, host string) confine.EgressRuleResult {
		layers := effectiveEgressApprovalRules(ctx, runtime, store, cmd)
		rule, ok := settings.EvaluateHostRuleLayers(layers, host)
		if !ok {
			return confine.EgressRuleResult{}
		}
		result := confine.EgressRuleResult{
			Pattern: rule.Pattern, UnitID: rule.Source.UnitID,
			PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
		}
		switch rule.Effect {
		case settings.ApprovalEffectDeny:
			result.Effect = confine.EgressRuleDeny
			recordEgressRuleDeny(ctx, runtime, cmd, host, rule)
		case settings.ApprovalEffectAsk:
			result.Effect = confine.EgressRuleAsk
		}
		return result
	})
	confine.SetEgressPosture(store.EgressPosture())
	confine.SetEgressPostureResolver(func(cmd confine.EgressCommand) confine.EgressPosture {
		return settings.EgressPostureFor(effectiveEgressApprovalConfig(store, cmd).Posture)
	})
	confine.SetApprovalsDisabledSource(func(cmd confine.EgressCommand) bool {
		cfg := effectiveEgressApprovalConfig(store, cmd)
		return cfg.NeverAsk != nil && *cfg.NeverAsk
	})
}

func recordEgressRuleDeny(
	ctx context.Context,
	runtime *Runtime,
	cmd confine.EgressCommand,
	host string,
	rule settings.ApprovalRule,
) {
	if runtime == nil || runtime.authzRecorder == nil {
		return
	}
	tool := strings.TrimSpace(cmd.Image)
	if tool == "" {
		tool = "command"
	}
	runtime.authzRecorder.AppendToolDenied(ctx, authzledger.ToolDeniedRecord{
		SessionID: cmd.SessionID, ParentSessionID: cmd.RootSessionID,
		Tool: tool, Args: map[string]any{"command": cmd.CommandLine, "host": host},
		ProjectDir: cmd.ProjectDir, RejectCode: "APPROVAL_RULE_DENIED",
		BlockReason: "APPROVAL_RULE_DENIED",
		ApprovalRules: []authzledger.ApprovalRuleCitation{{
			Category: string(rule.Category), Pattern: rule.Pattern, Effect: string(rule.Effect),
			UnitID: rule.Source.UnitID, PackID: rule.Source.PackID, Scope: string(rule.Source.Scope),
		}},
	})
}

func effectiveEgressApprovalRules(ctx context.Context, runtime *Runtime, store *settings.ApprovalStore, cmd confine.EgressCommand) settings.ApprovalRuleLayers {
	scope := llm.SettingsScopeGlobal
	ref := settings.ProjectRef{}
	if cmd.ProjectID != "" || cmd.ProjectDir != "" {
		scope = llm.SettingsScopeProject
		ref = settings.ProjectRef{ID: cmd.ProjectID, Dir: cmd.ProjectDir}
	}
	var source settings.ApprovalRuleCatalogSource
	if runtime != nil {
		source = runtime.approvalRules
	}
	return settings.EffectiveApprovalRuleLayers(ctx, store, source, scope, ref)
}

type buildDeps struct {
	boundary       *sandbox.Boundary
	git            *git.Manager
	statusCache    *atomic.Pointer[git.StatusCache]
	command        *hostcmd.Runner
	nativeConfig   nativemanifest.Config
	toolSchemas    *toolschema.Config
	readEscalation *surveytools.ReadEscalationStore
	surveyCatalog  *survey.Catalog
	fileAge        *fileage.Provider
	rerank         decide.Reranker
}

// nativeMutationTools are the file-mutating native tools that need post-build
// gate injection (edit review, plan sync).
type nativeMutationTools struct {
	read              *surveytools.ReadTool
	listDir           *surveytools.ListDirTool
	grep              *surveytools.GrepTool
	find              *surveytools.FindTool
	summarize         *surveytools.SummarizeTool
	write             *native.WriteTool
	edit              *native.EditTool
	replaceLines      *native.ReplaceLinesTool
	codeRewrite       *native.CodeRewriteTool
	restoreVersion    *native.RestoreVersionTool
	jqEdit            *native.JqEditTool
	commandTool       *native.CommandTool
	verifyTool        *native.VerifyTool
	commandOutputTool *native.CommandOutputTool
	commandStopTool   *native.CommandStopTool
	skillsRead        *skilltools.SkillsReadTool
}

// registerGitStatusTool wires native git_status with StatusCache force=true.
func registerGitStatusTool(reg *tools.DefaultRegistry, deps buildDeps) error {
	if !deps.nativeConfig.HasTool("git_status") || deps.git == nil {
		return nil
	}
	return reg.Register("git_status", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		page := gitStatusPage(args)
		cwd := tctx.ActiveRootPath()
		if page.Ignored {
			return gitStatusIgnoredOutput(ctx, deps.git, cwd, page, tctx)
		}
		// Fresh status includes writes from the current turn.
		var status *git.GitStatus
		var err error
		if deps.statusCache != nil {
			if cache := deps.statusCache.Load(); cache != nil {
				cached, e := cache.GetOrLoad(ctx, cwd, true)
				status, err = cached.Status, e
				if err == nil && status != nil {
					if subjects, subErr := cache.RecentSubjects(ctx, cwd, true, 5); subErr == nil {
						status.RecentCommits = subjects
					}
				}
				return gitStatusOutput(status, page, err, tctx)
			}
		}
		status, err = deps.git.Status(ctx, cwd)
		if err != nil {
			return gitStatusOutput(status, page, err, tctx)
		}
		if commits, logErr := deps.git.Log(ctx, cwd, git.GitLogOpts{Limit: 5}); logErr == nil && status != nil {
			for _, c := range commits {
				status.RecentCommits = append(status.RecentCommits, c.Subject)
			}
		}
		return gitStatusOutput(status, page, nil, tctx)
	})
}

func buildNativeRegistry(deps buildDeps) (*tools.DefaultRegistry, *nativeMutationTools, error) {
	reg, err := tools.NewCatalogRegistry(deps.toolSchemas)
	if err != nil {
		return nil, nil, err
	}
	boundary := deps.boundary

	register := func(name string, handler tools.ToolHandler) error {
		if !deps.nativeConfig.HasTool(name) {
			return nil
		}
		return reg.Register(name, handler)
	}

	readEscalation := surveytools.NewReadEscalationStore()
	if deps.readEscalation != nil {
		readEscalation = deps.readEscalation
	}
	catalog := sourcecatalog.Process()
	read := &surveytools.ReadTool{Boundary: boundary, Escalation: readEscalation, Age: deps.fileAge, Catalog: catalog}
	write := &native.WriteTool{Boundary: boundary}
	edit := &native.EditTool{Boundary: boundary}
	replaceLines := &native.ReplaceLinesTool{Boundary: boundary}
	codeRewrite := &native.CodeRewriteTool{Boundary: boundary}
	restoreVersion := &native.RestoreVersionTool{Boundary: boundary}
	find := &surveytools.FindTool{Boundary: boundary, Catalog: catalog}
	summarizeCaps, err := summarize.LoadCaps()
	if err != nil {
		return nil, nil, fmt.Errorf("summarize caps: %w", err)
	}
	summarizeTool := &surveytools.SummarizeTool{Boundary: boundary, Caps: summarizeCaps, Catalog: catalog, Rerank: deps.rerank}
	grep := &surveytools.GrepTool{Boundary: boundary, Catalog: catalog}
	jqTool := &nativejq.Tool{Boundary: boundary}
	jqEdit := &native.JqEditTool{Boundary: boundary}
	stat := &surveytools.StatTool{Boundary: boundary, Catalog: catalog}
	wc := &surveytools.WcTool{Boundary: boundary, Catalog: catalog}
	listDir := &surveytools.ListDirTool{Boundary: boundary, Catalog: catalog}
	chmod := &native.ChmodTool{Boundary: boundary}
	deleteTool := &native.DeleteTool{Boundary: boundary}
	copyTool := &native.CopyTool{Boundary: boundary}
	moveTool := &native.MoveTool{Boundary: boundary}
	mkdirTool := &native.MkdirTool{Boundary: boundary}
	diffTool := &surveytools.DiffTool{Boundary: boundary}
	extractArchiveTool := &native.ExtractArchiveTool{Boundary: boundary}
	chownTool := &native.ChownTool{Boundary: boundary}
	surveyRepo := &survey.RepoTool{
		Boundary: boundary, BaseCatalog: deps.surveyCatalog, Caps: survey.DefaultCaps(), SourceCatalog: catalog,
	}
	var commandTool *native.CommandTool
	var verifyTool *native.VerifyTool
	var commandOutputTool *native.CommandOutputTool
	var commandStopTool *native.CommandStopTool

	if err := registerCoreNativeTools(register, read, write, edit, replaceLines, codeRewrite, restoreVersion, find, summarizeTool, grep, jqTool, jqEdit, stat, wc, listDir, chmod, deleteTool, copyTool, moveTool, mkdirTool, diffTool, extractArchiveTool, chownTool, surveyRepo); err != nil {
		return nil, nil, err
	}

	if deps.command != nil {
		if err := registerCommandTools(reg, deps.command, boundary, &commandTool, &verifyTool, &commandOutputTool, &commandStopTool); err != nil {
			return nil, nil, err
		}
	}

	if err := registerGitStatusTool(reg, deps); err != nil {
		return nil, nil, err
	}
	if deps.nativeConfig.HasTool("git_diff") && deps.git != nil {
		if err := reg.Register("git_diff", gitDiffHandler(deps.git)); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_log") && deps.git != nil {
		if err := reg.Register("git_log", gitLogHandler(deps.git)); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_show") && deps.git != nil {
		showTool := &native.GitShowTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_show", showTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_blame") && deps.git != nil {
		blameTool := &native.GitBlameTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_blame", blameTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_ref") && deps.git != nil {
		refTool := &native.GitRefTool{Git: deps.git}
		if err := reg.Register("git_ref", refTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_branches") && deps.git != nil {
		branchesTool := &native.GitBranchesTool{Git: deps.git}
		if err := reg.Register("git_branches", branchesTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_restore") && deps.git != nil {
		restoreTool := &native.GitRestoreTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_restore", restoreTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if deps.nativeConfig.HasTool("git_commit") && deps.git != nil {
		commitTool := &native.GitCommitTool{Git: deps.git, Boundary: boundary}
		if err := reg.Register("git_commit", commitTool.Run); err != nil {
			return nil, nil, err
		}
	}
	if err := registerGitOperationTools(reg, deps, boundary); err != nil {
		return nil, nil, err
	}
	skillsRead := &skilltools.SkillsReadTool{}
	if err := register("skills_read", skillsRead.Run); err != nil {
		return nil, nil, err
	}
	sourceHistory := &surveytools.SourceHistoryTool{Boundary: boundary}
	if err := register("source_history", sourceHistory.Run); err != nil {
		return nil, nil, err
	}
	if len(reg.List()) == 0 {
		return nil, nil, fmt.Errorf("no native tools registered")
	}
	return reg, &nativeMutationTools{
		read:              read,
		listDir:           listDir,
		grep:              grep,
		find:              find,
		summarize:         summarizeTool,
		write:             write,
		edit:              edit,
		replaceLines:      replaceLines,
		codeRewrite:       codeRewrite,
		restoreVersion:    restoreVersion,
		jqEdit:            jqEdit,
		commandTool:       commandTool,
		verifyTool:        verifyTool,
		commandOutputTool: commandOutputTool,
		commandStopTool:   commandStopTool,
		skillsRead:        skillsRead,
	}, nil
}

func registerCommandTools(
	reg *tools.DefaultRegistry,
	runner *hostcmd.Runner,
	boundary *sandbox.Boundary,
	commandTool **native.CommandTool,
	verifyTool **native.VerifyTool,
	commandOutputTool **native.CommandOutputTool,
	commandStopTool **native.CommandStopTool,
) error {
	service, err := hostprocess.New()
	if err != nil {
		return err
	}
	processTools := &native.ProcessTools{Service: service}
	if err := reg.Register("process_list", processTools.List); err != nil {
		return err
	}
	if err := reg.Register("process_signal", processTools.Signal); err != nil {
		return err
	}
	ft := native.NewCommandFailureTracker()
	*commandTool = &native.CommandTool{Runner: runner, Boundary: boundary, FailureTracker: ft}
	*verifyTool = &native.VerifyTool{Runner: runner, Boundary: boundary, FailureTracker: ft}
	*commandOutputTool = &native.CommandOutputTool{}
	*commandStopTool = &native.CommandStopTool{}
	if err := reg.Register("command", (*commandTool).Run); err != nil {
		return err
	}
	if err := reg.Register("verify", (*verifyTool).Run); err != nil {
		return err
	}
	if err := reg.Register("command_output", (*commandOutputTool).Run); err != nil {
		return err
	}
	return reg.Register("command_stop", (*commandStopTool).Run)
}

func registerCoreNativeTools(
	register func(name string, handler tools.ToolHandler) error,
	read *surveytools.ReadTool,
	write *native.WriteTool,
	edit *native.EditTool,
	replaceLines *native.ReplaceLinesTool,
	codeRewrite *native.CodeRewriteTool,
	restoreVersion *native.RestoreVersionTool,
	find *surveytools.FindTool,
	summarizeTool *surveytools.SummarizeTool,
	grep *surveytools.GrepTool,
	jqTool *nativejq.Tool,
	jqEdit *native.JqEditTool,
	stat *surveytools.StatTool,
	wc *surveytools.WcTool,
	listDir *surveytools.ListDirTool,
	chmod *native.ChmodTool,
	deleteTool *native.DeleteTool,
	copyTool *native.CopyTool,
	moveTool *native.MoveTool,
	mkdirTool *native.MkdirTool,
	diffTool *surveytools.DiffTool,
	extractArchiveTool *native.ExtractArchiveTool,
	chownTool *native.ChownTool,
	surveyRepo *survey.RepoTool,
) error {
	for _, pair := range []struct {
		name    string
		handler tools.ToolHandler
	}{
		{"read", read.Run},
		{"write", write.Run},
		{"edit", edit.Run},
		{"replace_lines", replaceLines.Run},
		{"code_rewrite", codeRewrite.Run},
		{"restore_version", restoreVersion.Run},
		{"find", find.Run},
		{"summarize", summarizeTool.Run},
		{"grep", grep.Run},
		{nativejq.ToolName, jqTool.Run},
		{nativejq.EditToolName, jqEdit.Run},
		{"stat", stat.Run},
		{"wc", wc.Run},
		{"list_dir", listDir.Run},
		{"chmod", chmod.Run},
		{"delete", deleteTool.Run},
		{"copy", copyTool.Run},
		{"move", moveTool.Run},
		{"mkdir", mkdirTool.Run},
		{"chown", chownTool.Run},
		{"diff", diffTool.Run},
		{"extract_archive", extractArchiveTool.Run},
	} {
		if err := register(pair.name, pair.handler); err != nil {
			return err
		}
	}
	return register("survey_repo", surveyRepo.Run)
}
