package projectpaths

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// exactRelativeOperand retains OS component order for native operands. Logical
// aliases retain their existing lexical root-coordinate interpretation.
func exactRelativeOperand(resolved Resolved, operand string) (string, error) {
	if !filepath.IsAbs(operand) {
		return readRelative(resolved.ScopeRel), nil
	}
	root := filepath.Clean(resolved.Root.Path)
	prefix := root + string(filepath.Separator)
	if root == string(filepath.Separator) {
		prefix = root
	}
	if strings.HasPrefix(operand, prefix) {
		return operand[len(prefix):], nil
	}
	if operand == root {
		return ".", nil
	}
	// An OS alias may spell the root differently (for example a mounted alias).
	// Resolve only candidate root prefixes, leaving the remaining operand intact.
	for index := 0; index < len(operand); index++ {
		if !os.IsPathSeparator(operand[index]) {
			continue
		}
		candidate := operand[:index]
		if candidate != "" && sameDirectory(candidate, root) {
			return operand[index+1:], nil
		}
	}
	if sameDirectory(operand, root) {
		return ".", nil
	}
	return "", invalidOperand(operand, "native operand cannot be placed beneath its admitted root")
}

// authorityAddress maps a reached canonical name back to the same admitted root
// coordinates, so OS aliases cannot evade that root's read profile.
func authorityAddress(rootPath, actual string) (string, error) {
	rel, err := filepath.Rel(fspath.CanonicalPath(rootPath), actual)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", invalidOperand(actual, "reached identity left admitted root")
	}
	return filepath.Join(rootPath, rel), nil
}

// rootAddress keeps native aliases of attached roots inside that same root's
// scope checks, rather than treating their physical spelling as unrelated I/O.
func (p *ProgramPaths) rootAddress(operand string) string {
	if !filepath.IsAbs(operand) {
		return operand
	}
	best := operand
	longest := 0
	for _, root := range p.context.Source.Roots {
		canonical := fspath.CanonicalPath(root.Path)
		prefix := canonical + string(filepath.Separator)
		if canonical == root.Path {
			continue
		}
		if operand == canonical && len(canonical) > longest {
			best = root.Path
			longest = len(canonical)
		}
		if strings.HasPrefix(operand, prefix) && len(canonical) > longest {
			best = root.Path + string(filepath.Separator) + operand[len(prefix):]
			longest = len(canonical)
		}
	}
	return best
}

func sameDirectory(left, right string) bool {
	a, err := os.Stat(left)
	if err != nil || !a.IsDir() {
		return false
	}
	b, err := os.Stat(right)
	return err == nil && b.IsDir() && os.SameFile(a, b)
}
