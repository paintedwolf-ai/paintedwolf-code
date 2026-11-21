package blueprinttest

import (
	"context"
	"github.com/lycaon/lycaon/internal/blueprint"
)

// NewFileStore returns a FileStore that always resolves to projectDir.
func NewFileStore(projectDir string) *blueprint.FileStore {
	return blueprint.NewFileStore(func(_ context.Context, _ string) (string, error) {
		return projectDir, nil
	})
}
