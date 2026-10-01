package confine

import (
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/protectedpath"
)

// agentPolicyRootsSource supplies every registered project root, whose agent
// policy other sessions load even when this invocation is rooted elsewhere.
var agentPolicyRootsSource atomic.Pointer[func() []string]

// SetAgentPolicyRootsSource installs the registered project roots.
func SetAgentPolicyRootsSource(fn func() []string) {
	if fn == nil {
		agentPolicyRootsSource.Store(nil)
		return
	}
	agentPolicyRootsSource.Store(&fn)
}

// agentPolicyRoots returns the registered roots plus extra, canonical and unique.
func agentPolicyRoots(extra []string) []string {
	roots := append([]string(nil), extra...)
	if fn := agentPolicyRootsSource.Load(); fn != nil {
		roots = append(roots, (*fn)()...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		canonical := fspath.CanonicalPath(root)
		if canonical == "" || canonical == string(filepath.Separator) || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out
}

// AgentPolicyPath reports the loader location path falls in when it is agent
// policy below a registered project root or one of roots.
func AgentPolicyPath(path string, roots ...string) (protectedpath.AgentPolicyLocation, bool) {
	return AgentPolicyClassifier(roots...)(path)
}

// AgentPolicyClassifier resolves the roots once for classifying many paths.
func AgentPolicyClassifier(roots ...string) func(path string) (protectedpath.AgentPolicyLocation, bool) {
	resolved := agentPolicyRoots(roots)
	return func(path string) (protectedpath.AgentPolicyLocation, bool) {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return protectedpath.AgentPolicyLocation{}, false
		}
		p := fspath.CanonicalPath(path)
		for _, root := range resolved {
			if !PathAtOrUnder(p, root) || len(p) <= len(root) {
				continue
			}
			// The prefix matched, possibly with folded case; the tail is the relative path.
			rel := strings.TrimPrefix(p[len(root):], string(filepath.Separator))
			for _, location := range protectedpath.AgentPolicyLocations() {
				if location.Contains(rel) {
					return location, true
				}
			}
		}
		return protectedpath.AgentPolicyLocation{}, false
	}
}

// agentPolicySpecs protects every loader location below each project root.
func agentPolicySpecs(roots []string) []HardDenyWriteSpec {
	resolved := agentPolicyRoots(roots)
	if len(resolved) == 0 {
		return nil
	}
	var out []HardDenyWriteSpec
	for _, location := range protectedpath.AgentPolicyLocations() {
		for _, root := range resolved {
			switch {
			case location.Name != "":
				out = append(out, HardDenyWriteSpec{Layer: FloorAgentPolicy, Subpath: root, Regex: location.NameRegex()})
			case location.File != "":
				out = append(out, HardDenyWriteSpec{Layer: FloorAgentPolicy, Literal: fspath.CanonicalPath(location.Path(root))})
			default:
				out = append(out, HardDenyWriteSpec{Layer: FloorAgentPolicy, Subpath: fspath.CanonicalPath(location.Path(root))})
			}
		}
	}
	return out
}
