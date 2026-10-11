package settings

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/protectedpath"
)

// ApprovalCategoryAgentPolicy is a chat lease over changes to project files
// that steer or authorize agents.
const ApprovalCategoryAgentPolicy ApprovalCategory = "agent_policy"

// Agent-policy lease patterns name whole trust surfaces or exact files.
const (
	agentPolicySurfacesPrefix = "surfaces:"
	agentPolicyFilesPrefix    = "files:"
)

// agentPolicySurfaceCopy phrases each trust surface for coverage lines.
var agentPolicySurfaceCopy = map[string]string{
	protectedpath.SurfaceAgentsMD:             "project instructions (AGENTS.md)",
	protectedpath.SurfaceSkills:               "project skills",
	protectedpath.SurfacePromptOverrides:      "project prompt overrides",
	protectedpath.SurfaceProjectSettings:      "project approval and workflow settings",
	protectedpath.SurfaceProjectMCP:           "project MCP providers",
	protectedpath.SurfaceScanConfig:           "project scan settings",
	protectedpath.SurfaceExtensionConfig:      "project extension settings",
	protectedpath.SurfaceExtensionSuggestions: "project extension suggestions",
}

// agentPolicyPredicate is the lease a decision offers: whole trust surfaces
// below Strict, the exact files at Strict.
func agentPolicyPredicate(action hitl.ProposedAction, posture gate.Posture) ApprovalRule {
	var subjects []string
	prefix := agentPolicySurfacesPrefix
	for _, target := range action.Mutations.AgentPolicy {
		if posture.LeasesAgentPolicyFiles() {
			prefix = agentPolicyFilesPrefix
			subjects = append(subjects, agentPolicyFile(target, action.Scope.ProjectDir))
			continue
		}
		subjects = append(subjects, target.Surface)
	}
	slices.Sort(subjects)
	subjects = slices.Compact(subjects)
	if len(subjects) == 0 || slices.Contains(subjects, "") {
		return ApprovalRule{}
	}
	return ApprovalRule{Category: ApprovalCategoryAgentPolicy, Pattern: prefix + strings.Join(subjects, "\n"), Effect: ApprovalEffectAsk}
}

func agentPolicyFile(target hitl.AgentPolicyTarget, projectDir string) string {
	path := target.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectDir, path)
	}
	return fspath.CanonicalPath(path)
}

// agentPolicyCoverage describes a lease pattern in the card's coverage line.
func agentPolicyCoverage(pattern string) string {
	if files, ok := strings.CutPrefix(pattern, agentPolicyFilesPrefix); ok {
		return "changes to " + strings.ReplaceAll(files, "\n", ", ") + " until the chat is deleted"
	}
	surfaces, _ := strings.CutPrefix(pattern, agentPolicySurfacesPrefix)
	var named []string
	for _, surface := range strings.Split(surfaces, "\n") {
		if label, ok := agentPolicySurfaceCopy[surface]; ok {
			named = append(named, label)
		}
	}
	return "changes to " + strings.Join(named, ", ") + " until the chat is deleted"
}

// agentPolicyGrantCovers reports chat authority for every changed
// agent-policy file. At Strict only exact-file leases count, so a lease
// granted before a posture change cannot cover more than Strict would.
func agentPolicyGrantCovers(g *RuleApprovalGate, action hitl.ProposedAction, posture gate.Posture) bool {
	if g == nil || g.grants == nil || len(action.Mutations.AgentPolicy) == 0 {
		return false
	}
	grants := g.grants.live(action.Scope.ChatSession())
	for _, target := range action.Mutations.AgentPolicy {
		file := agentPolicyFile(target, action.Scope.ProjectDir)
		covered := false
		for _, grant := range grants {
			if agentPolicyGrantApplies(grant, action, posture, target.Surface, file) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func agentPolicyGrantApplies(grant hitl.ApprovalGrant, action hitl.ProposedAction, posture gate.Posture, surface, file string) bool {
	if grant.Predicate.Category != string(ApprovalCategoryAgentPolicy) || grant.Scope != hitl.ApprovalGrantScopeChat {
		return false
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now()) {
		return false
	}
	if !GrantScopeApplies(grant, action) {
		return false
	}
	if files, ok := strings.CutPrefix(grant.Predicate.Pattern, agentPolicyFilesPrefix); ok {
		return slices.Contains(strings.Split(files, "\n"), file)
	}
	if posture.LeasesAgentPolicyFiles() {
		return false
	}
	surfaces, ok := strings.CutPrefix(grant.Predicate.Pattern, agentPolicySurfacesPrefix)
	return ok && slices.Contains(strings.Split(surfaces, "\n"), surface)
}
