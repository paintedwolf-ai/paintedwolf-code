package confine

import (
	"sync"

	"github.com/lycaon/lycaon/internal/enginepaths"
)

var placementRegistry struct {
	sync.RWMutex
	folders func() []string
}

// SetPlacementFolders names the source of engine folders at volume roots that hold worker branches
// away from the state folder. Each is treated as the state folder is: closed to confined commands,
// with its worker-branch base left to the worker that owns a branch there.
func SetPlacementFolders(folders func() []string) {
	placementRegistry.Lock()
	defer placementRegistry.Unlock()
	placementRegistry.folders = folders
}

func placementFolders() []string {
	placementRegistry.RLock()
	folders := placementRegistry.folders
	placementRegistry.RUnlock()
	if folders == nil {
		return nil
	}
	return folders()
}

func placementWorkspaceRoots() []string {
	folders := placementFolders()
	roots := make([]string, 0, len(folders))
	for _, folder := range folders {
		roots = append(roots, enginepaths.WorkerBranchesRootUnder(folder))
	}
	return roots
}
