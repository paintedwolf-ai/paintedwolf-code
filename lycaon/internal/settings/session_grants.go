package settings

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostscope"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// sessionGrants stores explicit chat leases until the chat is disposed.
type sessionGrants struct {
	mu        sync.Mutex
	bySession scopedstore.Map[map[string]hitl.ApprovalGrant]
	byID      map[string]string
}

func newSessionGrants() *sessionGrants { return &sessionGrants{} }

func (s *sessionGrants) put(grant hitl.ApprovalGrant) bool {
	sessionID := grant.ChatSessionID
	if sessionID == "" || grant.ID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.bySession.Load(sessionID)
	if !ok || set == nil {
		set = map[string]hitl.ApprovalGrant{}
	}
	if existing, exists := set[grant.ID]; exists && (existing.ExpiresAt == nil || existing.ExpiresAt.After(time.Now())) {
		return false
	}
	set[grant.ID] = copySecretGrant(grant)
	s.bySession.Store(sessionID, set)
	if s.byID == nil {
		s.byID = map[string]string{}
	}
	s.byID[grant.ID] = sessionID
	if len(s.byID) > 512 {
		for id, candidateSessionID := range s.byID {
			if _, active := s.bySession.Load(candidateSessionID); !active {
				delete(s.byID, id)
			}
		}
	}
	return true
}

func (s *sessionGrants) matching(action hitl.ProposedAction, witness hitl.ApprovalGrantWitness) (hitl.ApprovalGrant, bool) {
	sessionID := action.ChatSession()
	if sessionID == "" {
		return hitl.ApprovalGrant{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.bySession.Load(sessionID)
	if !ok {
		return hitl.ApprovalGrant{}, false
	}
	now := time.Now()
	for _, grant := range set {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			continue
		}
		if hitl.WitnessEqual(grant.Witness, witness) && grantMatchesAction(grant, action) {
			return grant, true
		}
	}
	return hitl.ApprovalGrant{}, false
}

// live returns the chat's unexpired grants and drops the expired ones from
// the set.
func (s *sessionGrants) live(sessionID string) []hitl.ApprovalGrant {
	if sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.bySession.Load(sessionID)
	if !ok {
		return nil
	}
	return s.liveLocked(sessionID, set, time.Now())
}

// liveAll returns every chat's unexpired grants.
func (s *sessionGrants) liveAll() []hitl.ApprovalGrant {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []hitl.ApprovalGrant
	for sessionID, set := range s.bySession.Snapshot() {
		out = append(out, s.liveLocked(sessionID, set, now)...)
	}
	return out
}

func (s *sessionGrants) liveLocked(sessionID string, set map[string]hitl.ApprovalGrant, now time.Time) []hitl.ApprovalGrant {
	out := make([]hitl.ApprovalGrant, 0, len(set))
	for id, grant := range set {
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(now) {
			delete(set, id)
			delete(s.byID, id)
			continue
		}
		out = append(out, copySecretGrant(grant))
	}
	s.bySession.Store(sessionID, set)
	return out
}

func (s *sessionGrants) hasExactActionSet(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.bySession.Load(sessionID)
	if !ok {
		return false
	}
	for _, grant := range set {
		if len(grant.ExactActionSet) > 0 {
			return true
		}
	}
	return false
}

func (s *sessionGrants) revoke(id string) bool {
	return s.revokeInstalledBy(id, "")
}

func (s *sessionGrants) revokeInstalledBy(id, operationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, ok := s.byID[id]
	if !ok {
		return false
	}
	set, ok := s.bySession.Load(sessionID)
	if ok {
		grant, exists := set[id]
		if !exists || (operationID != "" && grant.OwnerOperationID != operationID) {
			return false
		}
		delete(set, id)
		s.bySession.Store(sessionID, set)
	}
	delete(s.byID, id)
	return true
}

func (s *sessionGrants) forget(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set, ok := s.bySession.Load(sessionID); ok {
		for id := range set {
			delete(s.byID, id)
		}
	}
	s.bySession.Delete(sessionID)
}

func grantMatchesAction(grant hitl.ApprovalGrant, action hitl.ProposedAction) bool {
	if grant.Predicate.Category == hitl.ApprovalGrantCategoryExecutionCapability || grant.GrantedPath != nil ||
		grant.Predicate.Category == string(ApprovalCategorySocketPath) ||
		grant.Predicate.Category == hitl.ApprovalGrantCategorySocketCapability ||
		grant.Predicate.Category == hitl.ApprovalGrantCategoryDirectIP ||
		grant.Predicate.Category == string(ApprovalCategoryHostResource) ||
		grant.Predicate.Category == string(ApprovalCategoryWriteRoot) ||
		grant.Predicate.Category == string(ApprovalCategoryAgentPolicy) {
		// Typed overlays match through their own cover functions.
		return false
	}
	if strings.TrimSpace(grant.ProjectID) != strings.TrimSpace(action.ProjectID) {
		return false
	}
	if len(grant.ExactActionSet) > 0 {
		key := hitl.GrantKey(action)
		if key == "" {
			return false
		}
		for _, allowed := range grant.ExactActionSet {
			if allowed == key {
				return true
			}
		}
		return false
	}
	rule := ApprovalRule{Category: ApprovalCategory(grant.Predicate.Category), Pattern: grant.Predicate.Pattern, Effect: ApprovalEffectAsk}
	if rule.Category == ApprovalCategoryMCP {
		// Provider identity is literal in a grant.
		return ingestion.IsMCPToolName(action.Tool) && action.ApprovalCategory == string(ApprovalCategoryMCP) &&
			strings.TrimSpace(action.ApprovalSubject) != "" && rule.Pattern == strings.TrimSpace(action.ApprovalSubject)
	}
	if rule.Category == ApprovalCategoryPath {
		return exactPathGrantMatches(grant, action)
	}
	if rule.Category == ApprovalCategoryTool {
		return rule.Pattern != "" && rule.Pattern == strings.TrimSpace(action.Tool)
	}
	if rule.Category == ApprovalCategoryCommand {
		return rule.Pattern == CommandTextFromActionArgs(action.Args)
	}
	if grant.Predicate.Category == hitl.ApprovalGrantCategoryEgressCommand {
		// The command's mediated network: any host, this exact command text.
		return action.Tool == "network" && strings.TrimSpace(action.Command) != "" &&
			strings.TrimSpace(action.Command) == strings.TrimSpace(rule.Pattern)
	}
	if grant.Predicate.Category == hitl.ApprovalGrantCategoryPackageCoordinate {
		return action.PackageExecution != nil && rule.Pattern != "" &&
			rule.Pattern == hitl.PackageCoordinatePattern(action.PackageExecution)
	}
	if rule.Category == ApprovalCategoryHost {
		host, _ := action.Args["host"].(string)
		host = strings.ToLower(strings.TrimSpace(host))
		site, port := hostscope.SplitTunnelPattern(rule.Pattern)
		if opaqueEgressAction(action.Args) {
			// Tunnel grants bind both site and port.
			return port != 0 && port == egressActionPort(action.Args) && matchHostPattern(site, host)
		}
		if port != 0 {
			return false
		}
		// Host lease coverage uses domain pattern matching.
		return matchHostPattern(site, host)
	}
	return ruleMatches(rule, action)
}

// Exact path grants cover every declared target without interpreting filename syntax.
func exactPathGrantMatches(grant hitl.ApprovalGrant, action hitl.ProposedAction) bool {
	pattern := exactGrantPath(grant.Predicate.Pattern, grant.ProjectDir)
	if pattern == "" || len(action.Files) == 0 {
		return false
	}
	files, root := action.Files, action.ProjectDir
	if action.ResolvedFiles != nil {
		if len(action.ResolvedFiles) != len(files) {
			return false
		}
		files, root = action.ResolvedFiles, ""
	}
	for _, file := range files {
		if action.ResolvedFiles != nil && !filepath.IsAbs(file) {
			return false
		}
		if exactGrantPath(file, root) != pattern {
			return false
		}
	}
	return true
}

func exactGrantPath(path, root string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && root != "" {
		path = filepath.Join(root, path)
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func grantedPathGrantCovers(g *RuleApprovalGate, durable []ApprovalGrant, action hitl.ProposedAction) bool {
	if g == nil {
		return false
	}
	targets := g.declaredFileTargets(action)
	if len(targets) == 0 {
		return false
	}
	// Every declared crossing requires coverage.
	for _, target := range targets {
		if !grantedPathCoversTarget(g, durable, action, target) {
			return false
		}
	}
	return true
}

func grantedPathCoversTarget(
	g *RuleApprovalGate, durable []ApprovalGrant, action hitl.ProposedAction, target *gate.FileTarget,
) bool {
	if g.grants != nil {
		for _, grant := range g.grants.live(action.ChatSession()) {
			if grantedPathGrantApplies(grant, action, target) {
				return true
			}
		}
	}
	for _, grant := range durable {
		if grantedPathGrantApplies(grant.ToDomain(), action, target) {
			return true
		}
	}
	return false
}

func grantedPathGrantApplies(grant hitl.ApprovalGrant, action hitl.ProposedAction, target *gate.FileTarget) bool {
	if grant.GrantedPath == nil || strings.TrimSpace(grant.GrantedPath.Path) == "" {
		return false
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now()) {
		return false
	}
	if !GrantScopeApplies(grant, action) {
		return false
	}
	if target.Mode == gate.ModeWrite && !grant.GrantedPath.Write {
		return false
	}
	// Sensitive targets require an exact path grant.
	if target.Sensitive || target.ProtectedSubject {
		return !grant.GrantedPath.Tree &&
			grantedpath.Normalize(grant.GrantedPath.Path) == grantedpath.Normalize(target.Path)
	}
	return grantedpath.CoversPath(grant.GrantedPath.Path, grant.GrantedPath.Tree, target.Path)
}

func writeRootGrantCovers(g *RuleApprovalGate, durable []ApprovalGrant, action hitl.ProposedAction) bool {
	root, _ := action.Args["proposed_write_root"].(string)
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	if g != nil && g.grants != nil {
		for _, grant := range g.grants.live(action.ChatSession()) {
			if writeRootGrantApplies(grant, action, root) {
				return true
			}
		}
	}
	for _, grant := range durable {
		if writeRootGrantApplies(grant.ToDomain(), action, root) {
			return true
		}
	}
	return false
}

func writeRootGrantApplies(grant hitl.ApprovalGrant, action hitl.ProposedAction, root string) bool {
	if grant.Predicate.Category != string(ApprovalCategoryWriteRoot) {
		return false
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now()) {
		return false
	}
	if strings.TrimSpace(grant.Predicate.Pattern) != root {
		return false
	}
	return GrantScopeApplies(grant, action)
}

func hostResourceGrantCovers(g *RuleApprovalGate, durable []ApprovalGrant, action hitl.ProposedAction) bool {
	if len(action.HostResources) == 0 {
		return false
	}
	covered := map[string]struct{}{}
	if g != nil && g.grants != nil {
		for _, grant := range g.grants.live(action.ChatSession()) {
			if hostResourceGrantApplies(grant, action) {
				addHostResourcePattern(covered, grant.Predicate.Pattern)
			}
		}
	}
	for _, grant := range durable {
		if hostResourceGrantApplies(grant.ToDomain(), action) {
			addHostResourcePattern(covered, grant.Pattern)
		}
	}
	for _, id := range action.HostResources {
		if _, ok := covered[strings.TrimSpace(id)]; !ok {
			return false
		}
	}
	return true
}

func hostResourceGrantApplies(grant hitl.ApprovalGrant, action hitl.ProposedAction) bool {
	if grant.Predicate.Category != string(ApprovalCategoryHostResource) {
		return false
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now()) {
		return false
	}
	return GrantScopeApplies(grant, action)
}

// GrantScopeApplies is the device / project / chat binding shared by typed overlays.
func GrantScopeApplies(grant hitl.ApprovalGrant, action hitl.ProposedAction) bool {
	switch grant.Scope {
	case hitl.ApprovalGrantScopeDevice:
		return true
	case hitl.ApprovalGrantScopeProject:
		return strings.TrimSpace(grant.ProjectID) != "" && strings.TrimSpace(grant.ProjectID) == strings.TrimSpace(action.ProjectID)
	case hitl.ApprovalGrantScopeChat:
		return strings.TrimSpace(grant.ChatSessionID) == strings.TrimSpace(action.ChatSession()) &&
			strings.TrimSpace(grant.ProjectID) == strings.TrimSpace(action.ProjectID)
	default:
		return false
	}
}

func addHostResourcePattern(covered map[string]struct{}, pattern string) {
	for _, id := range strings.Split(pattern, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			covered[id] = struct{}{}
		}
	}
}

func copySecretGrant(grant hitl.ApprovalGrant) hitl.ApprovalGrant {
	grant.ElevatedEffects = append(grant.ElevatedEffects[:0:0], grant.ElevatedEffects...)
	grant.SecretFingerprints = append([]string(nil), grant.SecretFingerprints...)
	grant.SecretNames = append([]string(nil), grant.SecretNames...)
	grant.SecretRecipients = append([]secretmatch.Recipient(nil), grant.SecretRecipients...)
	return grant
}
