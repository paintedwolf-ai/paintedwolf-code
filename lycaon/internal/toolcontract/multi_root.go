package toolcontract

import (
	"sort"
	"strings"
)

// MultiRootCapability classifies how a tool participates in multi-root path
// ergonomics, when more than one folder is attached.
//
// Compiled from the catalog's multi_root axis.
type MultiRootCapability int

const (
	// MultiRootNone has no path surface: orchestration, meta, and web tools.
	MultiRootNone MultiRootCapability = iota
	// MultiRootPathConsume resolves caller paths through projectpaths, so
	// `@label/x` addresses one root and an unknown label is refused.
	MultiRootPathConsume
	// MultiRootDiscovery union-walks every root on an empty or "." path.
	MultiRootDiscovery
	// MultiRootCommand runs a shell with an optional per-root cwd (cwd="@label").
	MultiRootCommand
)

func (c MultiRootCapability) String() string {
	switch c {
	case MultiRootPathConsume:
		return "path"
	case MultiRootDiscovery:
		return "discovery"
	case MultiRootCommand:
		return "command"
	default:
		return "none"
	}
}

// MCPToolNamePrefix marks a dynamic MCP tool, whose name the host does not own.
const MCPToolNamePrefix = "mcp_"

// MultiRootOf returns a tool's declared capability. The second result reports
// whether anyone declared one — an unlisted tool is unclassified, not `none`,
// and a gate that cannot tell those apart cannot fail closed on the first.
//
// MCP tool names are provider-supplied and unbounded, so they cannot be
// enumerated; the prefix is their declaration, and they carry no path surface.
func MultiRootOf(name string) (MultiRootCapability, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if capability, ok := compiledMultiRoot[name]; ok {
		return capability, true
	}
	if strings.HasPrefix(name, MCPToolNamePrefix) {
		return MultiRootNone, true
	}
	return MultiRootNone, false
}

// MultiRootDeclaredNames returns every explicitly classified tool, sorted, so a
// contract test can fail on an entry left behind by a rename or a deletion.
func MultiRootDeclaredNames() []string {
	out := make([]string, 0, len(compiledMultiRoot))
	for name := range compiledMultiRoot {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
