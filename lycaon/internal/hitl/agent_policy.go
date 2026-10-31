package hitl

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// AgentPolicyTarget is one changed file a project trust surface loads.
type AgentPolicyTarget struct {
	Path string
	// Surface is the trust surface id; chat leases may cover a whole surface.
	Surface string
}

// AgentPolicyTargetFor classifies a changed path. Relative paths resolve
// against projectDir; roots add invocation roots to the registered projects.
func AgentPolicyTargetFor(path, projectDir string, roots ...string) (AgentPolicyTarget, bool) {
	return AgentPolicyTargetClassifier(projectDir, roots...)(path)
}

// AgentPolicyTargetClassifier resolves the roots once for classifying many paths.
func AgentPolicyTargetClassifier(projectDir string, roots ...string) func(path string) (AgentPolicyTarget, bool) {
	projectDir = strings.TrimSpace(projectDir)
	classify := confine.AgentPolicyClassifier(append(roots, projectDir)...)
	return func(path string) (AgentPolicyTarget, bool) {
		path = strings.TrimSpace(path)
		if path == "" {
			return AgentPolicyTarget{}, false
		}
		abs := path
		if !filepath.IsAbs(abs) {
			if projectDir == "" {
				return AgentPolicyTarget{}, false
			}
			abs = filepath.Join(projectDir, abs)
		}
		location, ok := classify(abs)
		if !ok {
			return AgentPolicyTarget{}, false
		}
		return AgentPolicyTarget{Path: path, Surface: location.Surface}, true
	}
}

// AgentPolicyPaths lists the targets' paths in order.
func AgentPolicyPaths(targets []AgentPolicyTarget) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.Path)
	}
	return out
}
