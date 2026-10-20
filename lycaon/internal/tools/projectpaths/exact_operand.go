package projectpaths

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
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
		if operand[index] != byte(filepath.Separator) {
			continue
		}
		candidate := operand[:index]
		if candidate != "" && confine.PathEqual(fspath.CanonicalPath(candidate), fspath.CanonicalPath(root)) {
			return operand[index+1:], nil
		}
	}
	return "", invalidOperand(operand, "native operand cannot be placed beneath its admitted root")
}
