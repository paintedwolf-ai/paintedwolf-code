package fseffect

import (
	"fmt"
	"path/filepath"
	"strings"
)

// exactReadRelative validates representation without collapsing components.
// The contained descriptor walk handles dot/dotdot after resolving symlinks.
func exactReadRelative(rel string) (string, error) {
	if rel == "" {
		return ".", nil
	}
	if filepath.IsAbs(rel) || strings.IndexByte(rel, 0) >= 0 {
		return "", fmt.Errorf("%w: exact relative read required", ErrInvalidPath)
	}
	return rel, nil
}
