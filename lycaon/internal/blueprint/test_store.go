package blueprint

import "context"

// NewFileStoreForTest returns a FileStore that always resolves to projectDir.
func NewFileStoreForTest(projectDir string) *FileStore {
	return NewFileStore(func(_ context.Context, _ string) (string, error) {
		return projectDir, nil
	})
}
