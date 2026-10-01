package confine

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// FilesystemAccess names the operation a boundary verdict answers.
type FilesystemAccess string

const (
	AccessRead  FilesystemAccess = "read"
	AccessWrite FilesystemAccess = "write"
)

// FloorLayer places a filesystem deny in the reviewed-authority ordering.
type FloorLayer string

const (
	// FloorOutsideWriteRoots is the profile's default write deny.
	FloorOutsideWriteRoots FloorLayer = "outside_write_roots"
	// FloorBaseline protects read-only roots and the host config ancestor.
	// A validated reviewed root or managed workspace may override it.
	FloorBaseline FloorLayer = "baseline"
	// FloorProtected protects key and credential stores. Only an exact
	// protected-path transaction may override it.
	FloorProtected FloorLayer = "protected"
	// FloorAgentPolicy requires an invocation-specific approval naming the
	// trust-surface file.
	FloorAgentPolicy FloorLayer = "agent_policy"
	// FloorControlPlane protects the launcher, the host state tree, and raw devices.
	FloorControlPlane FloorLayer = "control_plane"
	// FloorReadConfigured holds per-action and operator read exclusions.
	FloorReadConfigured FloorLayer = "read_configured"
	// FloorReadKeyMaterial holds the key-material read floor.
	FloorReadKeyMaterial FloorLayer = "read_key_material"
	// FloorReadControlPlane holds the host state tree's read floor.
	FloorReadControlPlane FloorLayer = "read_control_plane"
)

// FloorGrant is how a recovery names the authority it requests.
type FloorGrant string

const (
	// GrantContainingDirectory asks for the directory that holds the path.
	GrantContainingDirectory FloorGrant = "containing_directory"
	// GrantExactPath asks for the path itself.
	GrantExactPath FloorGrant = "exact_path"
	// GrantRepositoryTree asks for the nearest enclosing repository work tree,
	// never the home directory or a top-level directory, and otherwise for the
	// containing directory, so a build writing across one checkout takes one
	// approval.
	GrantRepositoryTree FloorGrant = "repository_tree"
	// GrantLoaderTree asks for the path when its directory exists, and
	// otherwise for the nearest existing directory of the tree holding it, so
	// creating a whole skill or workflow takes one approval.
	GrantLoaderTree FloorGrant = "loader_tree"
)

// FloorRecovery is the declared route from a floor deny to an ask. A layer
// with no capability is system-terminal.
type FloorRecovery struct {
	// Capability is the capability_request field whose declaration asks.
	Capability string
	Grant      FloorGrant
}

// Terminal reports a layer that no approval can open.
func (r FloorRecovery) Terminal() bool { return r.Capability == "" }

// FloorLayers returns every layer a filesystem verdict can report.
func FloorLayers() []FloorLayer {
	return []FloorLayer{
		FloorOutsideWriteRoots, FloorBaseline, FloorProtected, FloorAgentPolicy, FloorControlPlane,
		FloorReadConfigured, FloorReadKeyMaterial, FloorReadControlPlane,
	}
}

// Recovery returns the route from this layer's deny to an ask.
func (l FloorLayer) Recovery() FloorRecovery {
	switch l {
	case FloorOutsideWriteRoots:
		return FloorRecovery{Capability: "write_root", Grant: GrantRepositoryTree}
	case FloorBaseline:
		return FloorRecovery{Capability: "write_root", Grant: GrantContainingDirectory}
	case FloorProtected:
		return FloorRecovery{Capability: "write_root", Grant: GrantExactPath}
	case FloorAgentPolicy:
		return FloorRecovery{Capability: "write_root", Grant: GrantLoaderTree}
	case FloorReadConfigured, FloorReadKeyMaterial:
		return FloorRecovery{Capability: "read_path", Grant: GrantExactPath}
	default:
		return FloorRecovery{}
	}
}

// GrantPath is the path a recovery names for a denied path below roots.
func (r FloorRecovery) GrantPath(path string, roots ...string) string {
	switch r.Grant {
	case GrantContainingDirectory:
		return filepath.Dir(path)
	case GrantRepositoryTree:
		return repositoryTreeGrant(path)
	case GrantLoaderTree:
		return loaderTreeGrant(path, roots)
	default:
		return path
	}
}

// FloorVerdict is the applied boundary's answer for one path.
type FloorVerdict struct {
	Allowed bool
	// Layer names the deny that decided a refused path.
	Layer FloorLayer
}

type fsOperation int

const (
	opRead fsOperation = iota
	opReadMetadata
	opWrite
)

func (op fsOperation) sbpl() string {
	switch op {
	case opRead:
		return "file-read*"
	case opReadMetadata:
		return "file-read-metadata"
	default:
		return "file-write*"
	}
}

// fsMatch is one SBPL path filter. Deny filters carry the layer they protect.
type fsMatch struct {
	literal string
	subpath string
	regex   string
	except  []string
	layer   FloorLayer
}

// fsRule is one SBPL filesystem block; later blocks override earlier ones.
type fsRule struct {
	op    fsOperation
	allow bool
	// everywhere matches every path, rendering an unfiltered block.
	everywhere bool
	matches    []fsMatch
}

// FilesystemRules is the ordered filesystem policy the profile renders. The
// same list answers host verdicts, so the two cannot disagree.
type FilesystemRules struct {
	rules    []fsRule
	patterns map[string]*regexp.Regexp
	// roots are the invocation's project roots, which agent-policy grants
	// resolve against.
	roots []string
}

func (r *FilesystemRules) add(rule fsRule) {
	if !rule.everywhere && len(rule.matches) == 0 {
		return
	}
	r.rules = append(r.rules, rule)
}

func subpathMatches(paths []string, layer FloorLayer) []fsMatch {
	out := make([]fsMatch, 0, len(paths))
	for _, p := range paths {
		out = append(out, fsMatch{subpath: p, layer: layer})
	}
	return out
}

func literalMatches(paths []string, layer FloorLayer) []fsMatch {
	out := make([]fsMatch, 0, len(paths))
	for _, p := range paths {
		out = append(out, fsMatch{literal: p, layer: layer})
	}
	return out
}

// render writes the rules; deny carries each deny block's report modifier.
func (r FilesystemRules) render(b *strings.Builder, deny string) {
	for _, rule := range r.rules {
		effect, modifier := "allow", ""
		if !rule.allow {
			effect, modifier = "deny", deny
		}
		if rule.everywhere {
			b.WriteString("(" + effect + " " + rule.op.sbpl() + modifier + ")\n")
			continue
		}
		b.WriteString("(" + effect + " " + rule.op.sbpl() + "\n")
		for _, m := range rule.matches {
			m.render(b)
		}
		if modifier != "" {
			b.WriteString("  " + strings.TrimSpace(modifier) + "\n")
		}
		b.WriteString(")\n")
	}
}

func (m fsMatch) render(b *strings.Builder) {
	switch {
	case m.literal != "":
		b.WriteString("  (literal " + sbplString(m.literal) + ")\n")
	case m.regex != "" && m.subpath != "":
		b.WriteString("  (require-all\n    (subpath " + sbplString(m.subpath) + ")\n")
		b.WriteString("    (regex " + sbplString(m.regex) + ")\n  )\n")
	case m.regex != "":
		b.WriteString("  (regex " + sbplString(m.regex) + ")\n")
	case len(m.except) == 0:
		if strings.TrimSpace(m.subpath) == "" {
			return
		}
		b.WriteString("  (subpath " + sbplString(m.subpath) + ")\n")
	default:
		if strings.TrimSpace(m.subpath) == "" {
			return
		}
		b.WriteString("  (require-all\n    (subpath " + sbplString(m.subpath) + ")\n")
		for _, path := range m.except {
			if strings.TrimSpace(path) == "" {
				continue
			}
			b.WriteString("    (require-not (subpath " + sbplString(path) + "))\n")
		}
		b.WriteString("  )\n")
	}
}

// Verdict answers whether the boundary permits access to path, reading the
// rules in profile order so the last matching block decides.
func (r FilesystemRules) Verdict(access FilesystemAccess, path string) FloorVerdict {
	path = fspath.CanonicalPath(strings.TrimSpace(path))
	if path == "" || !filepath.IsAbs(path) {
		return FloorVerdict{Allowed: true}
	}
	op := opRead
	verdict := FloorVerdict{Allowed: true}
	if access == AccessWrite {
		op = opWrite
		verdict = FloorVerdict{Layer: FloorOutsideWriteRoots}
	}
	for _, rule := range r.rules {
		if rule.op != op {
			continue
		}
		if rule.everywhere {
			verdict = FloorVerdict{Allowed: rule.allow}
			continue
		}
		for _, m := range rule.matches {
			if !r.matches(m, path) {
				continue
			}
			if rule.allow {
				verdict = FloorVerdict{Allowed: true}
			} else {
				verdict = FloorVerdict{Layer: m.layer}
			}
			break
		}
	}
	return verdict
}

func (r FilesystemRules) matches(m fsMatch, path string) bool {
	switch {
	case m.literal != "":
		return PathEqual(path, m.literal)
	case m.regex != "":
		return (m.subpath == "" || PathAtOrUnder(path, m.subpath)) && r.patterns[m.regex].MatchString(path)
	default:
		if !PathAtOrUnder(path, m.subpath) {
			return false
		}
		for _, except := range m.except {
			if PathAtOrUnder(path, except) {
				return false
			}
		}
		return true
	}
}

// maxSBPLStringBytes is the longest string literal the seatbelt profile reader
// accepts, counted after unescaping; a longer one fails the whole profile.
const maxSBPLStringBytes = 1023

// checkStringLengths refuses a filter the profile reader could not read, so
// the failure names the filter instead of surfacing from sandbox_init.
func (r FilesystemRules) checkStringLengths() error {
	for _, rule := range r.rules {
		for _, m := range rule.matches {
			for _, s := range append([]string{m.literal, m.subpath, m.regex}, m.except...) {
				if len(s) > maxSBPLStringBytes {
					return fmt.Errorf("confine: filesystem rule %.64q... is %d bytes; the profile reader accepts at most %d",
						s, len(s), maxSBPLStringBytes)
				}
			}
		}
	}
	return nil
}

// compilePatterns prepares regex filters for host verdicts. A filter Go cannot
// compile is refused, since the verdict could not answer what the kernel enforces.
func (r *FilesystemRules) compilePatterns() error {
	r.patterns = map[string]*regexp.Regexp{}
	for _, rule := range r.rules {
		for _, m := range rule.matches {
			if m.regex == "" {
				continue
			}
			if _, ok := r.patterns[m.regex]; ok {
				continue
			}
			pattern, err := regexp.Compile(m.regex)
			if err != nil {
				return fmt.Errorf("confine: filesystem rule %q: %w", m.regex, err)
			}
			r.patterns[m.regex] = pattern
		}
	}
	return nil
}
