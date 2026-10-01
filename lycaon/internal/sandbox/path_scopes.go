package sandbox

import (
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/config"
)

// PathScope is a named read/write path allowlist.
type PathScope struct {
	Description string
	Read        []string
	Write       []string
	Deny        []string
}

// PathScopeRegistry maps scope id → globs.
type PathScopeRegistry map[string]PathScope

type pathScopesFile struct {
	Scopes map[string]pathScopeEntry `yaml:"scopes"`
}

type pathScopeEntry struct {
	Description string   `yaml:"description"`
	Read        []string `yaml:"read"`
	Write       []string `yaml:"write"`
	Deny        []string `yaml:"deny"`
}

// LoadPathScopes reads config/packs/painted-wolf/platform/host/path-scopes.yaml.
func LoadPathScopes() (PathScopeRegistry, error) {
	data, err := config.Read(config.PathScopes)
	if err != nil {
		return nil, err
	}
	var raw pathScopesFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, err
	}
	out := make(PathScopeRegistry, len(raw.Scopes))
	for id, entry := range raw.Scopes {
		if id == "" {
			return nil, fmt.Errorf("path scope missing id")
		}
		out[id] = PathScope{
			Description: entry.Description,
			Read:        append([]string(nil), entry.Read...),
			Write:       append([]string(nil), entry.Write...),
			Deny:        append([]string(nil), entry.Deny...),
		}
	}
	return out, nil
}

// ResolveScopeGlobs returns read/write globs for a scope id.
func ResolveScopeGlobs(reg PathScopeRegistry, scopeID string) (read, write []string, err error) {
	if scopeID == "" {
		return nil, nil, fmt.Errorf("empty path scope id")
	}
	scope, ok := reg[scopeID]
	if !ok {
		return nil, nil, fmt.Errorf("unknown path scope %q", scopeID)
	}
	return append([]string(nil), scope.Read...), append([]string(nil), scope.Write...), nil
}

// CheckWriteInScope enforces named write-scope precedence:
//  1. Deny matches  → deny
//  2. Write matches → allow (allowlist; "**" = product-tree-wide)
//  3. otherwise     → deny (default-deny — empty Write does not allow)
func CheckWriteInScope(scope PathScope, scopeName, relPath string) error {
	relPath = filepath.ToSlash(relPath)
	if matchesScopeGlob(scope.Deny, relPath) {
		return newOutsideWriteScope(relPath, scopeName)
	}
	if matchesScopeGlob(scope.Write, relPath) {
		return nil
	}
	return newOutsideWriteScope(relPath, scopeName)
}
