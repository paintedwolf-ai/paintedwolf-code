package settings

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
)

func (g *RuleApprovalGate) currentActionFileAccess(action hitl.ProposedAction) []hitl.GrantedPathDelta {
	var access []hitl.GrantedPathDelta
	for _, target := range g.declaredFileTargets(action) {
		// Relative escapes remain invalid tool arguments, not grant subjects.
		if !target.OutsideRoots || !filepath.IsAbs(target.Path) {
			continue
		}
		access = append(access, hitl.GrantedPathDelta{
			Path: grantedpath.Normalize(target.Path), Write: target.Mode == gate.ModeWrite,
		})
	}
	return access
}

// Current-action authority covers the same exact directional subject during
// prepared effect review, without becoming a reusable grant or covering other gates.
func currentActionCoversTarget(action hitl.ProposedAction, target *gate.FileTarget) bool {
	path := grantedpath.Normalize(target.Path)
	for _, access := range action.Invocation.ApprovedFileAccess {
		if access.Path == path && (!access.Tree) && (target.Mode != gate.ModeWrite || access.Write) {
			return true
		}
	}
	return false
}

func (g *RuleApprovalGate) fileAccessCoversTargets(durable []ApprovalGrant, action hitl.ProposedAction) bool {
	targets := g.declaredFileTargets(action)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !currentActionCoversTarget(action, target) && !grantedPathCoversTarget(g, durable, action, target) {
			return false
		}
	}
	return true
}
