package workspacebaseline

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
)

// ID extracts a manifest's stable identity from its resolved runtime filename.
func ID(path string) (string, error) {
	name := filepath.Base(path)
	id := strings.TrimSuffix(name, ".db")
	parsed, err := uuid.Parse(id)
	if err != nil || name != id+".db" || parsed.String() != id {
		return "", fmt.Errorf("invalid workspace manifest identity")
	}
	return id, nil
}

// Path resolves a durable identity under the active installation.
func Path(dataDir, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || !filepath.IsAbs(dataDir) {
		return "", fmt.Errorf("invalid workspace manifest identity or data root")
	}
	return filepath.Join(dataDir, enginepaths.WorkerBaselinesDirName, id+".db"), nil
}

// ContentStore resolves the sibling object store from an already resolved manifest path.
// Persisted manifests carry content identities, never filesystem authority.
func ContentStore(manifestPath string) *sourceblob.Store {
	return sourceblob.New(filepath.Join(filepath.Dir(filepath.Dir(manifestPath)), enginepaths.SourceContentDirName))
}
