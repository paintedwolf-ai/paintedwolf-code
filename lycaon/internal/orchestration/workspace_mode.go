package orchestration

import "fmt"

// WorkspaceMode controls whether topology legs share a tree.
type WorkspaceMode string

const (
	WorkspaceShared   WorkspaceMode = "shared"
	WorkspaceIsolated WorkspaceMode = "isolated"
)

func effectiveWorkspaceMode(mode WorkspaceMode) WorkspaceMode {
	if mode == "" {
		return WorkspaceShared
	}
	return mode
}

func validateWorkspaceMode(mode WorkspaceMode) error {
	switch effectiveWorkspaceMode(mode) {
	case WorkspaceShared, WorkspaceIsolated:
		return nil
	default:
		return fmt.Errorf("unknown workspace_mode %q", mode)
	}
}
