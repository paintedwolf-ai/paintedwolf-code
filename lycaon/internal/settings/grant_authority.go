package settings

import (
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostscope"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

func (g *RuleApprovalGate) GrantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return g.offersForCard(action, result, true)
}

// AbsorbedGrantOffers omits the time rung supplied by the capability card.
func (g *RuleApprovalGate) AbsorbedGrantOffers(action hitl.ProposedAction, result *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return g.offersForCard(action, result, false)
}

func (g *RuleApprovalGate) offersForCard(action hitl.ProposedAction, result *hitl.ApprovalResult, ownsCard bool) []hitl.ApprovalGrantOffer {
	if g == nil || g.store == nil || !result.Required() {
		return nil
	}
	reuse := result.Decision.Reuse()
	if !reuse.Offered() {
		return nil
	}
	// Secret release uses exact fingerprint authority.
	for _, fired := range result.Decision.Gates() {
		if fired == api.GateSecretOutbound {
			return nil
		}
	}
	group := ""
	if !ownsCard {
		group = hitl.GroupAlsoAllow
	}
	if executionCapabilityDecision(result) {
		return g.executionCapabilityOffers(action, result, ownsCard, group)
	}
	if action.Execution.PackageExecution != nil && (slices.Contains(result.Decision.Gates(), api.GateRemotePackageExecution) || slices.Contains(result.Decision.Gates(), api.GateRemotePackageExecutionKnown)) {
		offers := withinReuse(g.packageCoordinateOffers(action, reuse, ownsCard, group), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	// Exact-action reuse takes precedence over family ladders.
	if reuse.Shape == gate.ReuseExactAction {
		offers := withinReuse(exactActionGrantOffers(action, reuse, ownsCard, group), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	if slices.Contains(result.Decision.Gates(), api.GateAgentPolicyChange) {
		predicate := agentPolicyPredicate(action, result.Decision.Posture)
		offers := withinReuse(g.grantOffersForPredicate(action, predicate, ownsCard, reuse, group), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	predicate := GrantPredicateForAction(action)
	if action.Invocation.Tool == "network" && opaqueEgressAction(action.Invocation.Args) && predicate.Pattern != "" {
		offers := withinReuse(g.opaqueEgressOffers(action, predicate, ownsCard, reuse, group), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	if predicate.Category == ApprovalCategoryWriteRoot && predicate.Pattern != "" {
		offers := withinReuse(g.grantOffersForPredicate(action, predicate, ownsCard, reuse, group), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	if gate.IsFilesystem(result.Decision.Primary) {
		target := g.declaredFileTarget(g.effectiveConfig(action), action)
		if target == nil {
			return g.hostResourceGroupOffers(action, result, false)
		}
		offers := GrantedPathOffers(action, *target, result.Decision, g.sources.Locations)
		return append(offers, g.hostResourceGroupOffers(action, result, false)...)
	}
	matchedHostResource := false
	matchedOrdinaryRule := false
	for _, matched := range result.MatchedRules {
		if matched.Category == string(ApprovalCategoryHostResource) {
			matchedHostResource = true
			continue
		}
		matchedOrdinaryRule = true
	}
	hostResourcePrimary := matchedHostResource && !matchedOrdinaryRule
	if hostResourcePrimary {
		predicate = hostResourceGrantPredicate(action)
	}
	if ownsCard && (predicate.Category == ApprovalCategoryCommand || predicate.Pattern == "") {
		offers := withinReuse(exactActionGrantOffers(action, reuse, true, ""), reuse)
		return append(offers, g.hostResourceGroupOffers(action, result, hostResourcePrimary)...)
	}
	offers := withinReuse(g.grantOffersForPredicate(action, predicate, ownsCard, reuse, group), reuse)
	return append(offers, g.hostResourceGroupOffers(action, result, hostResourcePrimary)...)
}

// hostResourceGroupOffers is the Host resources ladder. Reuse is predicate ×
// device, independent of the primary gate's ceiling.
func (g *RuleApprovalGate) hostResourceGroupOffers(
	action hitl.ProposedAction, result *hitl.ApprovalResult, hostResourcePrimary bool,
) []hitl.ApprovalGrantOffer {
	if hostResourcePrimary || len(action.Resources.HostResources) == 0 {
		return nil
	}
	hrReuse := gate.HostResourceReuse()
	hr := g.grantOffersForPredicate(action, hostResourceGrantPredicate(action), false, hrReuse, hitl.GroupHostResources)
	return withinReuse(hr, hrReuse)
}

// grantOffersForPredicate retains the durable slot even when its authority is unavailable.
func (g *RuleApprovalGate) grantOffersForPredicate(action hitl.ProposedAction, predicate ApprovalRule, mintsTimeRung bool, reuse gate.Reuse, group string) []hitl.ApprovalGrantOffer {
	if predicate.Category == ApprovalCategoryCommand || predicate.Pattern == "" {
		return nil
	}
	witness := hitl.BoundaryWitness(action.Execution.Contained)
	offers := make([]hitl.ApprovalGrantOffer, 0, 3)
	if mintsTimeRung {
		day := hitl.DayRung(g.makeGrantOffer(action, predicate, dayCarrierScope(reuse, action), witness, group))
		day.Group = group
		offers = append(offers, day)
	}
	offers = append(offers, g.makeGrantOffer(action, predicate, hitl.ApprovalGrantScopeChat, witness, group))
	durable := g.makeGrantOffer(action, predicate, durableSlotScope(predicate.Category, reuse), witness, group)
	return append(offers, durableSlotOffer(durable, action, reuse))
}

// durableSlotScope is the widest scope a predicate may carry: device for the
// machine-level subjects, project otherwise. The slot title follows the scope.
func durableSlotScope(cat ApprovalCategory, reuse gate.Reuse) hitl.ApprovalGrantScope {
	if scopeWithin(hitl.ApprovalGrantScopeDevice, reuse.Scope) && deviceRungCategory(cat) {
		return hitl.ApprovalGrantScopeDevice
	}
	return hitl.ApprovalGrantScopeProject
}

// durableSlotOffer keeps unavailable authority visible with its disabling reason.
func durableSlotOffer(durable hitl.ApprovalGrantOffer, action hitl.ProposedAction, reuse gate.Reuse) hitl.ApprovalGrantOffer {
	switch {
	case !action.Scope.HasProjectIdentity():
		return hitl.DisabledOffer(durable, hitl.NoteNoProjectOpen)
	case !scopeWithin(durable.Scope, reuse.Scope):
		return hitl.DisabledOffer(durable, hitl.NoteEndsWithChat)
	}
	return durable
}

// opaqueEgressOffers combines exact time grants with a port-bound site grant.
func (g *RuleApprovalGate) opaqueEgressOffers(action hitl.ProposedAction, family ApprovalRule, mintsTimeRung bool, reuse gate.Reuse, group string) []hitl.ApprovalGrantOffer {
	exact := gate.Reuse{Shape: reuse.Shape, Scope: gate.ScopeChat, DayCarrier: reuse.DayCarrier}
	offers := exactActionTimeRungs(action, exact, mintsTimeRung)
	witness := hitl.BoundaryWitness(action.Execution.Contained)
	durable := g.makeGrantOffer(action, family, hitl.ApprovalGrantScopeProject, witness, group)
	durable.ReaskWhen = hitl.ReaskWhenDifferentSiteOrPort
	durable.Grant.ReaskWhen = durable.ReaskWhen
	return groupOffers(append(offers, durableSlotOffer(durable, action, reuse)), group)
}

// deviceRungCategory reports whether a predicate subject may carry the 30-day
// rung. Tool is excluded: a month of a whole tool is too wide to offer.
func deviceRungCategory(cat ApprovalCategory) bool {
	switch cat {
	case ApprovalCategoryHost, ApprovalCategoryMCP, ApprovalCategoryPath,
		ApprovalCategoryWriteRoot, ApprovalCategoryHostResource:
		return true
	default:
		return false
	}
}

// dayCarrierScope returns the scope carried by the one-day option.
func dayCarrierScope(reuse gate.Reuse, action hitl.ProposedAction) hitl.ApprovalGrantScope {
	if reuse.DayScope() == gate.ScopeChat || !action.Scope.HasProjectIdentity() {
		return hitl.ApprovalGrantScopeChat
	}
	return hitl.ApprovalGrantScopeProject
}

// exactActionGrantOffers builds the ladder for byte-identical actions: day,
// chat, and the project slot — disabled in place when it cannot be picked.
func exactActionGrantOffers(action hitl.ProposedAction, reuse gate.Reuse, mintsTimeRung bool, group string) []hitl.ApprovalGrantOffer {
	offers := exactActionTimeRungs(action, reuse, mintsTimeRung)
	offers = append(offers, durableSlotOffer(hitl.ExactActionOfferAtScope(action, hitl.ApprovalGrantScopeProject), action, reuse))
	return groupOffers(offers, group)
}

// exactActionTimeRungs is the top of every exact-action ladder: the day rung
// when this card mints it, then the chat rung.
func exactActionTimeRungs(action hitl.ProposedAction, reuse gate.Reuse, mintsTimeRung bool) []hitl.ApprovalGrantOffer {
	offers := make([]hitl.ApprovalGrantOffer, 0, 3)
	if mintsTimeRung {
		offers = append(offers, hitl.DayRung(hitl.ExactActionOfferAtScope(action, dayCarrierScope(reuse, action))))
	}
	return append(offers, hitl.ExactActionOffer(action))
}

func groupOffers(offers []hitl.ApprovalGrantOffer, group string) []hitl.ApprovalGrantOffer {
	if group != "" {
		for i := range offers {
			offers[i].Group = group
		}
	}
	return offers
}

func (g *RuleApprovalGate) makeGrantOffer(action hitl.ProposedAction, rule ApprovalRule, scope hitl.ApprovalGrantScope, witness hitl.ApprovalGrantWitness, group string) hitl.ApprovalGrantOffer {
	now := time.Now().UTC()
	coverage := coverageForRule(rule)
	idChat, idProject, idWitness, reaskWhen := bindPredicateOfferIdentity(action, rule, scope, witness)
	grant := hitl.ApprovalGrant{
		Scope:         scope,
		Predicate:     hitl.ApprovalGrantPredicate{Category: string(rule.Category), Pattern: rule.Pattern},
		ChatSessionID: action.Scope.ChatSession(),
		ProjectID:     action.Scope.ProjectID,
		ProjectDir:    action.Scope.ProjectDir,
		Coverage:      coverage,
		GrantedAt:     now,
		Witness:       idWitness,
		ReaskWhen:     reaskWhen,
	}
	var rung hitl.ApprovalOptionRung
	switch scope {
	case hitl.ApprovalGrantScopeChat:
		rung = hitl.ApprovalRungChat
		grant.Title = hitl.TitleAllowForThisChat
		grant.ExpiresWhen = hitl.ExpiresWhenChatDeleted
	case hitl.ApprovalGrantScopeProject:
		rung = hitl.ApprovalRungProject
		expires := now.Add(hitl.ProjectLeaseDuration)
		grant.Title = hitl.TitleAllowForThisProject
		grant.ExpiresAt = &expires
		grant.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	case hitl.ApprovalGrantScopeDevice:
		rung = hitl.ApprovalRungDevice
		expires := now.Add(hitl.DeviceLeaseDuration)
		grant.Title = hitl.TitleAllowOnThisDevice
		grant.ExpiresAt = &expires
		grant.ExpiresWhen = hitl.ExpiresIn30DaysOrRevoked
		if deviceSpatialCategory(rule.Category) {
			grant.ChatSessionID = ""
		} else {
			grant.Coverage = coverage + hitl.DeviceCoverageSuffix
			coverage = grant.Coverage
		}
	}
	grant.ID = hitl.ApprovalGrantID(scope, string(rule.Category), rule.Pattern, idChat, idProject, idWitness, nil)
	return hitl.ApprovalGrantOffer{
		ID: grant.ID, Rung: rung, Scope: scope, Group: group, Title: grant.Title, Coverage: coverage,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Subject: gate.ReusePredicate, Grant: grant,
	}
}

// deviceSpatialCategory is a machine-local subject: device scope matches the
// named path or catalog ids on any project.
func deviceSpatialCategory(cat ApprovalCategory) bool {
	return cat == ApprovalCategoryHostResource || cat == ApprovalCategoryWriteRoot
}

// bindPredicateOfferIdentity sets grant-id bindings for one predicate offer.
func bindPredicateOfferIdentity(
	action hitl.ProposedAction, rule ApprovalRule, scope hitl.ApprovalGrantScope, witness hitl.ApprovalGrantWitness,
) (idChat, idProject string, idWitness hitl.ApprovalGrantWitness, reaskWhen string) {
	idChat, idProject, idWitness = action.Scope.ChatSession(), action.Scope.ProjectID, witness
	reaskWhen = "the destination, project, tool definition, or confinement changes"
	switch rule.Category {
	case ApprovalCategory(hitl.ApprovalGrantCategoryExecutionCapability):
		idWitness = hitl.ApprovalGrantWitness{}
		reaskWhen = "the permission expires or is revoked, the chat is deleted, or a different capability is requested"
	case ApprovalCategoryHostResource:
		idWitness = hitl.ApprovalGrantWitness{}
		switch scope {
		case hitl.ApprovalGrantScopeDevice:
			idChat, idProject = "", ""
			reaskWhen = "a different host resource is named"
		case hitl.ApprovalGrantScopeProject:
			idChat = ""
			reaskWhen = "a different host resource is named or the project changes"
		default:
			reaskWhen = "a different host resource is named or the chat is deleted"
		}
	case ApprovalCategoryWriteRoot:
		if scope == hitl.ApprovalGrantScopeDevice {
			idWitness = hitl.ApprovalGrantWitness{}
			idChat, idProject = "", ""
			reaskWhen = "a different write root is named"
		}
	case ApprovalCategoryAgentPolicy:
		idWitness = hitl.ApprovalGrantWitness{}
		reaskWhen = "a change reaches agent-policy files outside this approval or the chat is deleted"
	case ApprovalCategoryTool, ApprovalCategoryCommand, ApprovalCategoryMCP, ApprovalCategoryPath,
		ApprovalCategoryHost, ApprovalCategorySocketPath, ApprovalCategorySecret, ApprovalCategorySecretRedact,
		ApprovalCategoryActionSet, ApprovalCategoryPackageCoordinate:
	}
	return idChat, idProject, idWitness, reaskWhen
}

func coverageForRule(rule ApprovalRule) string {
	switch rule.Category {
	case ApprovalCategory(hitl.ApprovalGrantCategoryExecutionCapability):
		return hitl.ExecutionCapabilityCoverage(rule.Pattern)
	case ApprovalCategoryHost:
		return hostscope.Coverage(rule.Pattern)
	case ApprovalCategoryPath:
		return "operations on the exact path `" + rule.Pattern + "`"
	case ApprovalCategoryMCP:
		return "the `" + rule.Pattern + "` MCP action"
	case ApprovalCategoryWriteRoot:
		return "writes within `" + rule.Pattern + "`"
	case ApprovalCategoryAgentPolicy:
		return agentPolicyCoverage(rule.Pattern)
	case ApprovalCategoryHostResource:
		return "use of host resources `" + strings.ReplaceAll(rule.Pattern, ",", "`, `") + "`"
	default:
		return "the `" + rule.Pattern + "` tool"
	}
}

func (g *RuleApprovalGate) ApplyGrant(grant hitl.ApprovalGrant) (bool, error) {
	if err := hitl.ValidateElevatedEffects(grant.ElevatedEffects); err != nil {
		return false, err
	}
	if grant.Predicate.Category == hitl.ApprovalGrantCategoryExecutionCapability {
		if err := validateExecutionCapabilityGrant(grant); err != nil {
			return false, err
		}
	}
	if grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
		if err := hitl.ValidateSecretRelease(grant); err != nil {
			return false, err
		}
	}
	// Boundary capabilities store leases in their dedicated runtimes.
	if grant.Predicate.Category == hitl.ApprovalGrantCategorySocketCapability ||
		grant.Predicate.Category == hitl.ApprovalGrantCategoryDirectIP {
		return false, nil
	}
	grant.ExpiresAt = grant.ResolveExpiry(time.Now())
	if grant.Scope == hitl.ApprovalGrantScopeChat {
		return g.grants.put(grant), nil
	}
	persisted := ApprovalGrant{
		ID: grant.ID, Scope: grant.Scope, Category: ApprovalCategory(grant.Predicate.Category),
		Pattern: grant.Predicate.Pattern, ProjectID: grant.ProjectID, ProjectDir: grant.ProjectDir, Title: grant.Title,
		Coverage: grant.Coverage, GrantedAt: grant.GrantedAt, ExpiresAt: grant.ExpiresAt,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Witness: grant.Witness,
		GrantedPath:  grant.GrantedPath,
		ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Source: grant.Source,
		OwnerOperationID:    grant.OwnerOperationID,
		GrantedByPersonID:   grant.GrantedByPersonID,
		GrantedByPolicy:     grant.GrantedByPolicy,
		SecretFingerprints:  append([]string(nil), grant.SecretFingerprints...),
		SecretDestinationID: grant.SecretDestinationID,
		SecretRecipients:    append([]secretmatch.Recipient(nil), grant.SecretRecipients...),
		SecretNames:         append([]string(nil), grant.SecretNames...),
		ExactActionSet:      append([]string(nil), grant.ExactActionSet...),
		ElevatedEffects:     slices.Clone(grant.ElevatedEffects),
	}
	return g.store.UpsertGlobalGrant(persisted)
}

// approvalScopeRef resolves the settings layer an action is evaluated against.
// An action carrying either project identity reads the project layer.
func approvalScopeRef(action hitl.ProposedAction) (llm.SettingsScope, ProjectRef) {
	ref := ProjectRef{ID: action.Scope.ProjectID, Dir: action.Scope.ProjectDir}
	if ref.Dir != "" || ref.ID != "" {
		return llm.SettingsScopeProject, ref
	}
	return llm.SettingsScopeGlobal, ref
}

// effectiveConfig is the merged device-and-project approval policy for one action.
func (g *RuleApprovalGate) effectiveConfig(action hitl.ProposedAction) ApprovalConfig {
	if g == nil || g.store == nil {
		return ApprovalConfig{}
	}
	return g.store.Get(approvalScopeRef(action))
}

func (g *RuleApprovalGate) GrantCovers(action hitl.ProposedAction) bool {
	if g == nil || g.store == nil {
		return false
	}
	return g.actionLeaseCovers(g.effectiveConfig(action), action)
}

func (g *RuleApprovalGate) HostResourceLeaseCovers(action hitl.ProposedAction) bool {
	if g == nil || g.store == nil || len(action.Resources.HostResources) == 0 {
		return false
	}
	return hostResourceGrantCovers(g, g.effectiveConfig(action).Grants, action)
}

func (g *RuleApprovalGate) RevokeGrant(id string) (bool, error) {
	if g.grants != nil && g.grants.revoke(id) {
		return true, nil
	}
	return g.store.RevokeGlobalGrant(id)
}

func (g *RuleApprovalGate) RevokeGrantInstalledBy(id, operationID string) (bool, error) {
	if g.grants != nil && g.grants.revokeInstalledBy(id, operationID) {
		return true, nil
	}
	return g.store.RevokeGlobalGrantInstalledBy(id, operationID)
}

// ListGrants returns chat and durable leases visible to a chat.
func (g *RuleApprovalGate) ListGrants(chatSessionID string) []hitl.ApprovalGrant {
	var out []hitl.ApprovalGrant
	if chatSessionID == "" {
		out = g.grants.liveAll()
	} else {
		out = g.grants.live(chatSessionID)
	}
	for _, grant := range g.store.GlobalGrants() {
		out = append(out, grant.ToDomain())
	}
	return out
}

func (grant ApprovalGrant) ToDomain() hitl.ApprovalGrant {
	return hitl.ApprovalGrant{
		ID: grant.ID, Scope: grant.Scope,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(grant.Category), Pattern: grant.Pattern},
		ProjectID: grant.ProjectID, ProjectDir: grant.ProjectDir, Title: grant.Title, Coverage: grant.Coverage,
		GrantedAt: grant.GrantedAt, ExpiresAt: grant.ExpiresAt, ExpiresWhen: grant.ExpiresWhen,
		ReaskWhen: grant.ReaskWhen, Witness: grant.Witness, GrantedPath: grant.GrantedPath,
		ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Source: grant.Source,
		OwnerOperationID:    grant.OwnerOperationID,
		GrantedByPersonID:   grant.GrantedByPersonID,
		GrantedByPolicy:     grant.GrantedByPolicy,
		SecretFingerprints:  append([]string(nil), grant.SecretFingerprints...),
		SecretDestinationID: grant.SecretDestinationID,
		SecretRecipients:    append([]secretmatch.Recipient(nil), grant.SecretRecipients...),
		SecretNames:         append([]string(nil), grant.SecretNames...),
		ExactActionSet:      append([]string(nil), grant.ExactActionSet...),
		ElevatedEffects:     slices.Clone(grant.ElevatedEffects),
	}
}

func matchingDurableGrant(grants []ApprovalGrant, action hitl.ProposedAction, witness hitl.ApprovalGrantWitness) (ApprovalGrant, bool) {
	for _, grant := range grants {
		if hitl.WitnessEqual(grant.Witness, witness) && grantMatchesAction(grant.ToDomain(), action) {
			return grant, true
		}
	}
	return ApprovalGrant{}, false
}

// withinReuse applies the decision's subject and scope ceilings.
func withinReuse(offers []hitl.ApprovalGrantOffer, reuse gate.Reuse) []hitl.ApprovalGrantOffer {
	if !reuse.Offered() {
		return nil
	}
	out := make([]hitl.ApprovalGrantOffer, 0, len(offers))
	for _, offer := range offers {
		// A disabled slot already states why it is out of reach.
		if offer.Disabled {
			out = append(out, offer)
			continue
		}
		// Time-boxed offers use the day carrier ceiling.
		ceiling := reuse.Scope
		if offer.TTLSeconds > 0 {
			ceiling = reuse.DayScope()
		}
		if shapeWithin(offer.Subject, reuse.Shape) && scopeWithin(offer.Scope, ceiling) {
			out = append(out, offer)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shapeWithin(offer, ceiling gate.ReuseShape) bool {
	rank := map[gate.ReuseShape]int{
		gate.ReuseExactAction: 1, gate.ReusePredicate: 2,
	}
	return rank[offer] > 0 && rank[offer] <= rank[ceiling]
}

func scopeWithin(scope hitl.ApprovalGrantScope, ceiling gate.Scope) bool {
	rank := map[hitl.ApprovalGrantScope]int{
		hitl.ApprovalGrantScopeChat: 1, hitl.ApprovalGrantScopeProject: 2, hitl.ApprovalGrantScopeDevice: 3,
	}
	ceilingRank := map[gate.Scope]int{gate.ScopeChat: 1, gate.ScopeProject: 2, gate.ScopeDevice: 3}
	return rank[scope] <= ceilingRank[ceiling]
}

func (g *RuleApprovalGate) packageCoordinateOffers(action hitl.ProposedAction, reuse gate.Reuse, mintsTimeRung bool, group string) []hitl.ApprovalGrantOffer {
	if action.Execution.PackageExecution == nil || len(action.Execution.PackageExecution.Packages) == 0 {
		return nil
	}
	pattern := hitl.PackageCoordinatePattern(action.Execution.PackageExecution)
	if pattern == "" {
		return nil
	}
	rule := ApprovalRule{
		Category: ApprovalCategory(hitl.ApprovalGrantCategoryPackageCoordinate),
		Pattern:  pattern,
	}
	offers := g.grantOffersForPredicate(action, rule, mintsTimeRung, reuse, group)
	pkgSummary := packageNamesSummary(action.Execution.PackageExecution)
	coverage := "execution of package " + pkgSummary + " for this project"
	reaskWhen := "a different package version is requested or revoked"
	for i := range offers {
		offers[i].Coverage = coverage
		offers[i].ReaskWhen = reaskWhen
		offers[i].Grant.Coverage = coverage
		offers[i].Grant.ReaskWhen = reaskWhen
	}
	return offers
}

func packageNamesSummary(exec *packageexec.Execution) string {
	if exec == nil || len(exec.Packages) == 0 {
		return "code"
	}
	names := make([]string, 0, len(exec.Packages))
	for _, pkg := range exec.Packages {
		name := strings.TrimSpace(pkg.Name)
		version := strings.TrimSpace(pkg.ResolvedVersion)
		if version == "" {
			version = strings.TrimSpace(pkg.RequestedVersion)
		}
		if version != "" {
			name += "@" + version
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}
