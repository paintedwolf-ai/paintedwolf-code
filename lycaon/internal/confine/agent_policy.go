package confine

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/protectedpath"
)

type agentPolicyRegistration struct{ roots func() []string }

var (
	agentPolicyMu     sync.RWMutex
	agentPolicySource *agentPolicyRegistration
)

// SetAgentPolicyRootsSource installs registered roots until its owner releases it.
func SetAgentPolicyRootsSource(fn func() []string) func() {
	registration := &agentPolicyRegistration{roots: fn}
	agentPolicyMu.Lock()
	agentPolicySource = registration
	agentPolicyMu.Unlock()
	return func() {
		agentPolicyMu.Lock()
		defer agentPolicyMu.Unlock()
		if agentPolicySource == registration {
			agentPolicySource = nil
		}
		registration.roots = nil
	}
}

// agentPolicyRoots returns the registered roots plus extra, canonical and unique.
func agentPolicyRoots(extra []string) []string {
	roots := append([]string(nil), extra...)
	agentPolicyMu.RLock()
	var read func() []string
	if agentPolicySource != nil {
		read = agentPolicySource.roots
	}
	agentPolicyMu.RUnlock()
	if read != nil {
		roots = append(roots, read()...)
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
