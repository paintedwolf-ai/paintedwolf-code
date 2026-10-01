package sourceview

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AssertScanPathWithinProject rejects paths that escape projectDir.
func AssertScanPathWithinProject(projectDir, target string) error {
	projectDir = strings.TrimSpace(projectDir)
	target = strings.TrimSpace(target)
	if projectDir == "" {
		return fmt.Errorf("project dir is required")
	}
	if target == "" {
		return fmt.Errorf("scan target path is required")
	}

	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("project dir: %w", err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("scan target: %w", err)
	}
	absProject, err = filepath.EvalSymlinks(absProject)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	absTarget, err = filepath.EvalSymlinks(absTarget)
	if err != nil {
		return fmt.Errorf("resolve scan target: %w", err)
	}

	rel, err := filepath.Rel(absProject, absTarget)
	if err != nil {
		return fmt.Errorf("scan target outside project: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("scan target %q outside project root", target)
	}
	return nil
}
