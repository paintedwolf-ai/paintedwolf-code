package config

import (
	"strings"
)

// Bundled configuration paths shared by their consumers.
const (
	// StockPacks is the shipped extension-pack namespace.
	StockPacks Rel = "packs/painted-wolf"
	// StockMeta is the stock suite manifest.
	StockMeta = StockPacks + "/meta.yaml"

	// Platform pack.
	Platform            = StockPacks + "/platform"
	PlatformManifest    = Platform + "/" + PackManifestName
	PlatformHost        = Platform + "/host"
	PlatformAgents      = Platform + "/agents"
	PlatformPrompts     = PlatformAgents + "/prompts"
	PlatformGuidance    = Platform + "/guidance"
	PlatformPolicy      = Platform + "/policy"
	PlatformTools       = Platform + "/tools"
	NativeTools         = PlatformTools + "/native-tools.yaml"
	LycaonTools         = PlatformTools + "/lycaon-tools.yaml"
	ToolSchemasDir      = PlatformTools + "/schemas"
	ToolProfilesDir     = PlatformTools + "/profiles"
	PlatformShared      = Platform + "/shared"
	SharedPartials      = PlatformShared + "/partials"
	SharedArchetypes    = PlatformShared + "/archetypes"
	PlatformFlows       = Platform + "/workflows"
	PlatformApprovals   = Platform + "/approvals"
	EvidenceKinds       = PlatformGuidance + "/evidence-kinds.yaml"
	GateFeedbackDir     = PlatformGuidance + "/gate-feedback"
	GateFeedbackTmplDir = Platform + "/guidance/gate-feedback-templates"
	SharedIntake        = PlatformShared + "/intake"
	SharedWorkflowDiag  = PlatformShared + "/workflow-diagnostics"

	// Platform host shells — operator YAML each subsystem reads by a fixed path.
	Providers          = PlatformHost + "/providers.yaml"
	PricingSources     = PlatformHost + "/pricing-sources.yaml"
	ToolsScope         = PlatformHost + "/tools-scope.yaml"
	ModelPolicy        = PlatformHost + "/model-policy.yaml"
	Compaction         = PlatformHost + "/compaction.yaml"
	ComposePolicy      = PlatformHost + "/compose-policy.yaml"
	CoordinatorFlow    = PlatformHost + "/coordinator-flow.yaml"
	ContentAuthority   = PlatformHost + "/content-authority.yaml"
	DesignKitUsage     = PlatformHost + "/design-kit-usage.yaml"
	CoordinatorSurface = PlatformHost + "/coordinator-surfaces.yaml"
	Decisions          = PlatformHost + "/decisions.yaml"
	DecisionRelease    = PlatformHost + "/decision-release.json"
	SurfaceProfiles    = PlatformHost + "/surface-profiles.yaml"
	DistroMCP          = PlatformHost + "/distro-mcp.yaml"
	MCPRecipes         = PlatformHost + "/mcp-recipes.yaml"
	HostResources      = PlatformHost + "/host-resources.yaml"
	PathScopes         = PlatformHost + "/path-scopes.yaml"
	UserPath           = PlatformHost + "/user-path.yaml"
	PromptBudgets      = PlatformHost + "/prompt-budgets.yaml"
	RenderBudgets      = PlatformHost + "/render-budgets.yaml"
	SourceParsing      = PlatformHost + "/source-parsing.yaml"
	SessionLimits      = PlatformHost + "/session.yaml"
	SessionPostures    = PlatformHost + "/session-postures.yaml"
	StackRegistryDocs  = PlatformHost + "/stack-registry-docs.yaml"
	Workers            = PlatformHost + "/workers.yaml"
	AgentToolProfiles  = PlatformHost + "/agent-tool-profiles.yaml"
	DisclosurePolicy   = PlatformHost + "/disclosure-policy.yaml"
	ModelContextWindow = PlatformHost + "/model-context-windows.yaml"
	ProjectPolicy      = PlatformHost + "/project-policy.yaml"
	Review             = PlatformHost + "/review.yaml"
	FileBriefing       = PlatformHost + "/file-briefing.yaml"
	Summarize          = PlatformHost + "/summarize.yaml"
	AnchorsDir         = PlatformHost + "/anchors"
	AnchorCatalog      = AnchorsDir + "/catalog.yaml"
	OARProfile         = AnchorsDir + "/oar-profile.yaml"
	PostureRulesDir    = PlatformHost + "/posture-rules"
	SecretPatternsDir  = PlatformHost + "/secret-patterns"
	UserNoticesDir     = PlatformHost + "/user-notices"
	ClientNoticesDir   = PlatformHost + "/client-notices"

	// Security pack.
	SecurityHost             = StockPacks + "/security/host"
	SecurityApprovals        = SecurityHost + "/approvals.yaml"
	ApprovalOutcomeCodes     = SecurityHost + "/approval-outcome-codes.yaml"
	SandboxProfile           = SecurityHost + "/sandbox.yaml"
	ScannerCatalog           = SecurityHost + "/scanner-catalog.yaml"
	DetectionPacksDir        = SecurityHost + "/detection-packs"
	DetectionPackUpstreamDir = SecurityHost + "/detection-pack-upstream"
	DetectionActionSemantics = SecurityHost + "/detection-action-semantics.yaml"
	DetectionApprovalCorpus  = SecurityHost + "/detection-approval-corpus.yaml"
	Grounding                = SecurityHost + "/grounding.yaml"
	ProgressGatedTools       = SecurityHost + "/progress-gated-tools.yaml"
	ConsequenceBandPaths     = SecurityHost + "/consequence-band.yaml"
	AskTriggerPathsDir       = SecurityHost + "/ask-triggers"
	SecretMintDir            = SecurityHost + "/secret-mint"
	SecretMintDefaults       = SecretMintDir + "/defaults.yaml"
	SecretMintAssignments    = SecretMintDir + "/mint-assignments.yaml"
	SecretMintFixtures       = SecretMintDir + "/fixtures.yaml"
	SecretMintProvenance     = SecretMintDir + "/zxcvbn-provenance.yaml"
	SecretMintVendorDir      = SecretMintDir + "/vendor"
	Audit                    = SecurityHost + "/audit.yaml"
	PackageExecutionManagers = SecurityHost + "/package-execution-managers.yaml"
	// PackageRegistries lists the public package registries per language.
	PackageRegistries = SecurityHost + "/package-registries.yaml"
	// SecretPlaceholders lists documented example credentials the outbound
	// screen never treats as a secret.
	SecretPlaceholders = SecurityHost + "/secret-placeholders.yaml"

	// Web-research pack.
	WebResearchHost      = StockPacks + "/web-research/host"
	WebResearchProviders = WebResearchHost + "/web-research-providers.yaml"
	WebResearchLimits    = WebResearchHost + "/web-research-limits.yaml"

	// Runtime scanners — engine wiring rather than pack content.
	// SourceScope bounds what the source plane reads from a project root.
	SourceScope Rel = "runtime/source/scope.yaml"
	// SourceDirectoryPriority orders catalog discovery and names the trees a
	// recursive expansion leaves closed, without excluding paths.
	SourceDirectoryPriority Rel = "runtime/source/directory-priority.yaml"

	ScannersDir          Rel = "runtime/scanners"
	BundledScanners          = ScannersDir + "/bundled-manifest.yaml"
	ScannerGates             = ScannersDir + "/gates.yaml"
	OpengrepGates            = ScannersDir + "/opengrep-gates.yaml"
	ScanExcludes             = ScannersDir + "/scan-excludes.yaml"
	ScanHints                = ScannersDir + "/scan-hints.yaml"
	ScanIgnores              = ScannersDir + "/ignores.yaml"
	SecurityScanners         = ScannersDir + "/security-scanners.yaml"
	ScannerRulesDir          = ScannersDir + "/rules"
	SecretRulesDir           = ScannersDir + "/secret-rules"
	KingfisherRulesDir       = SecretRulesDir + "/vendor/kingfisher"
	KingfisherManifest       = KingfisherRulesDir + "/manifest.yaml"
	KingfisherCatalogDir     = KingfisherRulesDir + "/rules"
	ScannerRunner            = ScannersDir + "/runner.yaml"
	ScannersList             = ScannersDir + "/scanners.yaml"

	// Runtime, non-scanner.
	ModelRoleExclusions Rel = "runtime/model-role-exclusions.yaml"
	SearchGrammar       Rel = "runtime/search/grammar.json"
	StorageDensity      Rel = "runtime/storage/density.yaml"
	StorageDebug        Rel = "runtime/storage/debug.yaml"
	StorageWorkerBranch Rel = "runtime/storage/worker-branches.yaml"

	// Runtime fixture.
	FixturesDir Rel = "fixtures"
	MockLLM         = FixturesDir + "/mock_llm.yaml"

	// Implement pack.
	ImplementDefaultSpawn = StockPacks + "/implement/host/implement-default-spawn.yaml"

	// Plan pack.
	PlanBlueprintFields = StockPacks + "/plan/host/plan-blueprint-fields.yaml"

	// GitEnginePin is the pinned Git toolchain descriptor.
	GitEnginePin Rel = "gitengine/pin.yaml"

	// Names, not paths — joined onto a discovered directory.
	PackManifestName     = "extension.yaml"
	WorkflowManifestName = "workflow.yaml"
	WorkflowRegistryFile = "registry.yaml"
	PersonaContractFile  = "_persona-contract.yaml"
)

var consumedPackYAMLFiles = map[Rel]struct{}{
	EvidenceKinds: {},
	PlatformFlows + "/" + WorkflowRegistryFile: {},
	Providers:                {},
	PricingSources:           {},
	ToolsScope:               {},
	ModelPolicy:              {},
	Compaction:               {},
	ComposePolicy:            {},
	CoordinatorFlow:          {},
	ContentAuthority:         {},
	DesignKitUsage:           {},
	CoordinatorSurface:       {},
	Decisions:                {},
	SurfaceProfiles:          {},
	DistroMCP:                {},
	MCPRecipes:               {},
	HostResources:            {},
	PathScopes:               {},
	UserPath:                 {},
	PromptBudgets:            {},
	RenderBudgets:            {},
	SourceParsing:            {},
	SessionLimits:            {},
	SessionPostures:          {},
	StackRegistryDocs:        {},
	Workers:                  {},
	AgentToolProfiles:        {},
	DisclosurePolicy:         {},
	ModelContextWindow:       {},
	ProjectPolicy:            {},
	Review:                   {},
	FileBriefing:             {},
	Summarize:                {},
	AnchorCatalog:            {},
	OARProfile:               {},
	SecurityApprovals:        {},
	ApprovalOutcomeCodes:     {},
	SandboxProfile:           {},
	ScannerCatalog:           {},
	DetectionActionSemantics: {},
	DetectionApprovalCorpus:  {},
	Grounding:                {},
	ProgressGatedTools:       {},
	ConsequenceBandPaths:     {},
	Audit:                    {},
	PackageExecutionManagers: {},
	PackageRegistries:        {},
	SecretPlaceholders:       {},
	WebResearchProviders:     {},
	WebResearchLimits:        {},
	ImplementDefaultSpawn:    {},
	PlanBlueprintFields:      {},
	SecretMintDefaults:       {},
	SecretMintAssignments:    {},
	SecretMintFixtures:       {},
	SecretMintProvenance:     {},
	DetectionPackUpstreamDir + "/structured-cloud-actions.yaml": {},
}

var consumedPackYAMLDirs = []Rel{
	PostureRulesDir,
	SecretPatternsDir,
	ClientNoticesDir,
	AskTriggerPathsDir,
}

// ConsumesPackYAML reports whether a fixed-path subsystem consumes path.
func ConsumesPackYAML(path Rel) bool {
	path = Rel(path.String())
	if _, ok := consumedPackYAMLFiles[path]; ok {
		return true
	}
	for _, dir := range consumedPackYAMLDirs {
		if strings.HasPrefix(path.String(), dir.String()+"/") {
			return true
		}
	}
	return false
}

// Pack is the bundled root of a stock pack, e.g. Pack("plan").
func Pack(leaf string) Rel { return StockPacks.Join(leaf) }
