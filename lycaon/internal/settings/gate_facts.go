package settings

import (
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sensitivepath"
)

// DetectionSource supplies posture-filtered detection matches.
type DetectionSource interface {
	MatchAction(action hitl.ProposedAction, posture gate.Posture) (hitl.DetectionMatch, bool)
}

// MCPToolPinSource reports tool-definition drift.
type MCPToolPinSource interface {
	ToolDefinitionChanged(toolName string) bool
}

// Sources are the approval gate's fact producers.
type Sources struct {
	// Detections supplies additive approval matches.
	Detections func() DetectionSource
	// Pins reports MCP tool-definition drift.
	Pins MCPToolPinSource
	// Locations supplies sensitive-path matches.
	Locations *sensitivepath.Catalog
	// ApprovalRules supplies trust-gated extension rules.
	ApprovalRules ApprovalRuleCatalogSource
}

// NoSources reports successful empty fact producers.
func NoSources() Sources {
	return Sources{Detections: func() DetectionSource { return inertDetections{} }, Pins: inertPins{}, ApprovalRules: inertApprovalRules{}}
}

// A nil Detections source remains an unreported producer.
func (s Sources) normalized() Sources {
	if s.Pins == nil {
		s.Pins = inertPins{}
	}
	if s.ApprovalRules == nil {
		s.ApprovalRules = inertApprovalRules{}
	}
	return s
}

type inertDetections struct{}

func (inertDetections) MatchAction(hitl.ProposedAction, gate.Posture) (hitl.DetectionMatch, bool) {
	return hitl.DetectionMatch{}, false
}

type inertPins struct{}

func (inertPins) ToolDefinitionChanged(string) bool { return false }

// factsForAction assembles pre-spawn facts and their citations.
func (g *RuleApprovalGate) factsForAction(
	action hitl.ProposedAction, cfg ApprovalConfig, layers ApprovalRuleLayers,
) (gate.Facts, *hitl.DetectionMatch, []ApprovalRule) {
	var citation *hitl.DetectionMatch
	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran: gate.ProducerContainment | gate.ProducerLease | gate.ProducerRule |
			gate.ProducerConsent | gate.ProducerFilePath,
		Containment:   containmentFor(action),
		ProcessAccess: action.Execution.ProcessAccess,
		Recoverable:   ClassifyTier(action) != TierIrreversible,
	}
	facts.ExecutionCapabilityLeased = g.executionCapabilityCovers(action)
	facts.AgentPolicyPaths = hitl.AgentPolicyPaths(action.Mutations.AgentPolicy)
	// Preserve the distinction between absent and empty detection results.
	var detections DetectionSource
	if read := g.sources.Detections; read != nil {
		detections = read()
	}
	if detections != nil {
		facts.Ran |= gate.ProducerDetection
		if m, ok := detections.MatchAction(action, cfg.Posture); ok {
			facts.Detection = detectionFact(m)
			citation = &m
		}
	}
	if action.Execution.PackageExecution != nil {
		facts.Ran |= gate.ProducerPackageExecution
		facts.PackageExecution = packageExecutionFact(action)
	}

	// Action, host-resource, and granted-path leases cover separate authority.
	actionLeased := g.actionLeaseCovers(cfg, action)
	hostCovered := len(action.Resources.HostResources) == 0 || hostResourceGrantCovers(g, cfg.Grants, action)
	facts.Leased = actionLeased && hostCovered
	facts.LeasedExact = g.actionExactLeaseCovers(cfg, action)
	facts.LeasedPackage = g.actionPackageLeaseCovers(cfg, action)
	facts.FileLeased = grantedPathGrantCovers(g, cfg.Grants, action)
	facts.AgentPolicyLeased = agentPolicyGrantCovers(g, action, cfg.Posture)
	var matchedRules []ApprovalRule
	for _, rules := range [][]ApprovalRule{layers.Device, layers.Project} {
		matches, primary := effectiveAskRulesForLayer(rules, action, actionLeased, hostCovered)
		matchedRules = append(matchedRules, matches...)
		if facts.UserRule == nil && primary != nil {
			facts.UserRule = primary
		}
	}
	facts.File = g.declaredFileTarget(cfg, action)
	if tool := strings.TrimSpace(action.Invocation.Tool); ingestion.IsMCPToolName(tool) {
		facts.MCP = &gate.MCPCall{Tool: tool}
		if g.sources.Pins.ToolDefinitionChanged(tool) {
			facts.Consent = &gate.Consent{Tool: tool, DefinitionChanged: true}
		}
	}
	return facts, citation, matchedRules
}

func packageExecutionFact(action hitl.ProposedAction) *gate.PackageExecution {
	if action.Execution.PackageExecution == nil {
		return nil
	}
	source := action.Execution.PackageExecution
	out := &gate.PackageExecution{
		Manager: source.Manager, Operation: string(source.Operation),
	}
	for _, pkg := range source.Packages {
		version := pkg.ResolvedVersion
		if version == "" {
			version = pkg.RequestedVersion
		}
		out.Packages = append(out.Packages, gate.PackageIdentity{
			System: pkg.System, Name: pkg.Name, Version: version, AgeDays: pkg.AgeDays,
			Status: string(pkg.Status), SourceRepository: pkg.SourceRepository,
			VerifiedAttestation: pkg.VerifiedAttestation,
		})
	}
	return out
}

func effectiveAskRulesForLayer(
	rules []ApprovalRule,
	action hitl.ProposedAction,
	actionLeased, hostCovered bool,
) ([]ApprovalRule, *gate.UserRule) {
	var matches []ApprovalRule
	var primary *gate.UserRule
	if rule, subject, ok := askPolicyRuleFor(rules, action, actionLeased, hostCovered); ok {
		matches = append(matches, rule)
		fact := gate.UserRule{Category: string(rule.Category), Pattern: rule.Pattern, Subject: subject}
		primary = &fact
	}
	if !hostCovered {
		if rule, ok := matchHostResourceRules(rules, action.Resources.HostResources, action.Resources.HostResourceFamilies); ok &&
			rule.Effect == ApprovalEffectAsk && !containsApprovalRule(matches, rule) {
			matches = append(matches, rule)
			if primary == nil {
				fact := gate.UserRule{
					Category: string(rule.Category), Pattern: rule.Pattern,
					Subject: strings.Join(action.Resources.HostResources, ", "),
				}
				primary = &fact
			}
		}
	}
	return matches, primary
}

func containsApprovalRule(rules []ApprovalRule, candidate ApprovalRule) bool {
	for _, rule := range rules {
		if rule.Category == candidate.Category && rule.Pattern == candidate.Pattern &&
			rule.Effect == candidate.Effect && rule.Source == candidate.Source {
			return true
		}
	}
	return false
}

// declaredFileTarget returns the highest-risk uncovered crossing.
func (g *RuleApprovalGate) declaredFileTarget(cfg ApprovalConfig, action hitl.ProposedAction) *gate.FileTarget {
	targets := g.declaredFileTargets(action)
	if len(targets) == 0 {
		return nil
	}
	// Strict reviews the write itself, so scratch authority stands for reads only.
	if cfg.Posture.ReviewsConfinedWrites() {
		for _, target := range targets {
			if target.Mode == gate.ModeWrite {
				target.WithinConfinement = false
			}
		}
	}
	pool := make([]*gate.FileTarget, 0, len(targets))
	for _, target := range targets {
		if !grantedPathCoversTarget(g, cfg.Grants, action, target) {
			pool = append(pool, target)
		}
	}
	if len(pool) == 0 {
		pool = targets
	}
	worst := worstFileTarget(pool)
	if len(targets) == 1 {
		return worst
	}
	// Copy the target before adding batch display metadata.
	withCount := *worst
	withCount.OtherTargetCount = len(targets) - 1
	return &withCount
}

// worstFileTarget picks the highest-risk crossing among candidates.
func worstFileTarget(candidates []*gate.FileTarget) *gate.FileTarget {
	worst := candidates[0]
	worstRank := fileTargetSeverityRank(worst)
	for _, target := range candidates[1:] {
		if rank := fileTargetSeverityRank(target); rank > worstRank {
			worst, worstRank = target, rank
		}
	}
	return worst
}

// fileTargetSeverityRank orders crossings by disclosure severity.
func fileTargetSeverityRank(t *gate.FileTarget) int {
	rank := 0
	if t.OutsideRoots && !t.WithinConfinement {
		rank = 1
	}
	if t.Sensitive {
		rank = 2
	}
	if t.ProtectedSubject {
		rank = 3
	}
	return rank
}

// declaredFileWrites reports whether the action's files are write targets and
// whether it declares files at all. A command declares none before it runs,
// where confinement answers for the process; its prepared stream writes reach
// review as writes, like a native write's.
func declaredFileWrites(action hitl.ProposedAction) (write, declared bool) {
	if IsCommandToolName(action.Invocation.Tool) {
		return true, len(action.Mutations.FileChanges) > 0
	}
	return IsPathMutatingTool(action.Invocation.Tool), true
}

// declaredFileTargets returns every declared path crossing.
func (g *RuleApprovalGate) declaredFileTargets(action hitl.ProposedAction) []*gate.FileTarget {
	write, declared := declaredFileWrites(action)
	if !declared {
		return nil
	}
	var targets []*gate.FileTarget
	roots := workspaceRoots(action)
	for _, raw := range action.Invocation.Files {
		path := strings.TrimSpace(raw)
		// Worker-branch paths are an OAR reject.
		if path == "" || enginepaths.IsWorkerBranchPath(path) {
			continue
		}
		mode := gate.ModeRead
		catalogMode := sensitivepath.ModeRead
		if write {
			mode, catalogMode = gate.ModeWrite, sensitivepath.ModeWrite
		}
		target := &gate.FileTarget{
			Path: path, Mode: mode,
			OutsideRoots: PathEscapesRoots(roots, path),
		}
		// Scratch and cache authority comes from the subprocess filesystem boundary.
		if len(action.Execution.Contained.WriteRoots) > 0 {
			target.WithinConfinement = confine.PathWithinWriteRoots(path, action.Execution.Contained.WriteRoots)
		} else if root, err := confine.FilesystemRootForPath(action.Scope.ProjectID, roots, nil, action.Scope.SessionScratchRoot, path); err == nil {
			target.WithinConfinement = root != ""
		}
		// ClassifyResolved covers sensitive descendants and path aliases.
		if m, ok := g.sources.Locations.ClassifyResolved(path, catalogMode); ok {
			target.Sensitive = true
			target.CatalogID = m.ID
			target.CatalogTitle = m.Title
			target.ProtectedSubject = m.Protected
		}
		// Credential files ask on write inside roots too; overlays cannot remove the class.
		if mode == gate.ModeWrite && protectedpath.IsCredentialFile(path) {
			target.ProtectedSubject = true
		}
		// Protected writes and key material remain reviewable inside roots.
		subject := confine.ClassifyBlockedWrite(path)
		if subject.Kind == confine.WriteSubjectKeyMaterial ||
			(subject.Kind != confine.WriteSubjectOrdinary && mode == gate.ModeWrite) {
			target.ProtectedSubject = true
		} else if mode == gate.ModeRead && !target.OutsideRoots {
			target.ProtectedSubject = false
		}
		if target.OutsideRoots || target.ProtectedSubject {
			targets = append(targets, target)
		}
	}
	return targets
}

// containmentFor subtracts authorized network capabilities from boundary facts.
func containmentFor(action hitl.ProposedAction) gate.Containment {
	c := gate.Containment{
		ProcessControl: action.Execution.Contained.ProcessControl, HostExecution: action.Execution.Contained.HostExecution,
		SpawnsProcess:     isProcessSpawningTool(action.Invocation.Tool),
		FSJailed:          action.Execution.Contained.FSJailed,
		Egress:            action.Execution.Contained.Egress,
		DirectIP:          action.Execution.Contained.DirectIP || action.Execution.Contained.Egress == hitl.ContainedEgressDirectIP,
		SocketCount:       action.Execution.Contained.SocketCount,
		SocketPathsDigest: action.Execution.Contained.SocketPathsDigest,
	}
	if action.Egress.AuthorizedDirectIP {
		c.DirectIP = false
	}
	if c.SocketCount == len(action.Sockets.SocketGrants) && c.SocketPathsDigest == confine.SocketPathsDigest(action.Sockets.SocketGrants) {
		authorized := make(map[string]bool, len(action.Sockets.AuthorizedSocketDigests))
		for _, digest := range action.Sockets.AuthorizedSocketDigests {
			authorized[digest] = true
		}
		var uncovered []confine.SocketGrant
		for _, grant := range action.Sockets.SocketGrants {
			if !authorized[confine.SocketPathsDigest([]confine.SocketGrant{grant})] {
				uncovered = append(uncovered, grant)
			}
		}
		c.SocketCount = len(uncovered)
		c.SocketPathsDigest = confine.SocketPathsDigest(uncovered)
	}
	return c
}

func detectionFact(m hitl.DetectionMatch) *gate.Match {
	return &gate.Match{
		PackID:    m.PackID,
		RuleID:    m.RuleID,
		RuleTitle: m.RuleTitle,
		Level:     m.Level,
		// Untagged matches use the stricter interpretation.
		External:      m.External || !m.Tagged,
		Local:         m.Local,
		Unrecoverable: m.Unrecoverable || !m.Tagged,
	}
}

// askPolicyRuleFor returns the primary ask rule for an action.
func askPolicyRuleFor(rules []ApprovalRule, action hitl.ProposedAction, actionLeased, hostCovered bool) (ApprovalRule, string, bool) {
	units := commandUnitsFromArgs(action.Invocation.Args)
	if !actionLeased && IsCommandToolName(action.Invocation.Tool) && len(units) > 0 {
		if rule, matched, ok := matchCommandRules(rules, units); ok && rule.Effect == ApprovalEffectAsk {
			return rule, matched, true
		}
	}
	if !actionLeased {
		if _, rule, ok := matchGeneralRules(rules, action); ok && rule.Effect == ApprovalEffectAsk {
			return rule, action.Invocation.Tool, true
		}
	}
	if hostCovered {
		return ApprovalRule{}, "", false
	}
	// Tool and command rules remain the card's primary rule.
	if rule, ok := matchHostResourceRules(rules, action.Resources.HostResources, action.Resources.HostResourceFamilies); ok &&
		rule.Effect == ApprovalEffectAsk {
		return rule, strings.Join(action.Resources.HostResources, ", "), true
	}
	return ApprovalRule{}, "", false
}

func effectiveDenyRulesForLayer(rules []ApprovalRule, action hitl.ProposedAction) []ApprovalRule {
	var matches []ApprovalRule
	units := commandUnitsFromArgs(action.Invocation.Args)
	if rule, ok := matchHostResourceRules(rules, action.Resources.HostResources, action.Resources.HostResourceFamilies); ok &&
		rule.Effect == ApprovalEffectDeny {
		matches = append(matches, rule)
	}
	if IsCommandToolName(action.Invocation.Tool) && len(units) > 0 {
		if rule, _, ok := matchCommandRules(rules, units); ok && rule.Effect == ApprovalEffectDeny {
			return append(matches, rule)
		}
	}
	if _, rule, ok := matchGeneralRules(rules, action); ok && rule.Effect == ApprovalEffectDeny {
		matches = append(matches, rule)
	}
	return matches
}

// actionLeaseCovers reports reusable authority for an unchanged boundary.
func (g *RuleApprovalGate) actionLeaseCovers(cfg ApprovalConfig, action hitl.ProposedAction) bool {
	if writeRootGrantCovers(g, cfg.Grants, action) {
		return true
	}
	if grantedPathGrantCovers(g, cfg.Grants, action) {
		return true
	}
	witness := hitl.BoundaryWitness(action.Execution.Contained)
	if g.grants != nil {
		if _, ok := g.grants.matching(action, witness); ok {
			return true
		}
	}
	if _, ok := matchingDurableGrant(cfg.Grants, action, witness); ok {
		return true
	}
	return false
}

// actionExactLeaseCovers reports exact-action authority.
func (g *RuleApprovalGate) actionExactLeaseCovers(cfg ApprovalConfig, action hitl.ProposedAction) bool {
	witness := hitl.BoundaryWitness(action.Execution.Contained)
	if g.grants != nil {
		if grant, ok := g.grants.matching(action, witness); ok && len(grant.ExactActionSet) > 0 {
			return true
		}
	}
	if grant, ok := matchingDurableGrant(cfg.Grants, action, witness); ok && len(grant.ExactActionSet) > 0 {
		return true
	}
	return false
}

// actionPackageLeaseCovers reports package coordinate predicate authority.
// The lease binds coordinates, scope, and expiry only; the registry-only
// execution boundary applies to every run, so the witness is not part of it.
func (g *RuleApprovalGate) actionPackageLeaseCovers(cfg ApprovalConfig, action hitl.ProposedAction) bool {
	if action.Execution.PackageExecution == nil || len(action.Execution.PackageExecution.Packages) == 0 {
		return false
	}
	pattern := hitl.PackageCoordinatePattern(action.Execution.PackageExecution)
	if pattern == "" {
		return false
	}
	if g.grants != nil {
		for _, grant := range g.grants.live(action.Scope.ChatSession()) {
			if grant.Predicate.Category == hitl.ApprovalGrantCategoryPackageCoordinate &&
				grant.Predicate.Pattern == pattern &&
				GrantScopeApplies(grant, action) {
				return true
			}
		}
	}
	for _, grant := range cfg.Grants {
		dGrant := grant.ToDomain()
		if dGrant.Predicate.Category == hitl.ApprovalGrantCategoryPackageCoordinate &&
			dGrant.Predicate.Pattern == pattern &&
			GrantScopeApplies(dGrant, action) {
			return true
		}
	}
	return false
}
