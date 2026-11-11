package enginepaths

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// WorkerPlacementsFileName is the state-folder registry of placement folders.
const WorkerPlacementsFileName = "worker-placements.json"

// WorkerPlacementDirPrefix starts every engine-managed folder outside the state
// folder. Walkers skip these names, so a root attached above one never sees it.
const WorkerPlacementDirPrefix = ".paintedwolf-placement-"

// The suffix asks Spotlight to leave the folder out of its index.
const workerPlacementDirSuffix = ".noindex"

// WorkerPlacementDirName names one user's folder for one engine state folder, so
// a second installation on the same volume never sweeps this one's branches.
func WorkerPlacementDirName(uid int, stateRoot string) string {
	return WorkerPlacementDirPrefix + strconv.Itoa(uid) + "-" + ProjectKey(stateRoot) + workerPlacementDirSuffix
}

// IsWorkerPlacementDirName reports an engine-managed placement folder name.
func IsWorkerPlacementDirName(name string) bool {
	return strings.HasPrefix(name, WorkerPlacementDirPrefix)
}

// WorkerPlacementFolderOf returns the placement folder containing path, or "".
func WorkerPlacementFolderOf(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	for dir := path; ; dir = filepath.Dir(dir) {
		if IsWorkerPlacementDirName(filepath.Base(dir)) {
			return dir
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}

var workerPlacementFolders atomic.Pointer[[]string]

// PublishWorkerPlacementFolders tells path rewriting which folders hold branches.
// A volume name may contain the spaces that end a rewrite token, so the known
// prefix is removed first rather than inferred from the surrounding text.
func PublishWorkerPlacementFolders(folders []string) {
	slashed := make([]string, 0, len(folders))
	for _, folder := range folders {
		slashed = append(slashed, filepath.ToSlash(filepath.Clean(folder)))
	}
	workerPlacementFolders.Store(&slashed)
}

func withoutWorkerPlacementPrefixes(s string) string {
	folders := workerPlacementFolders.Load()
	if folders == nil {
		return s
	}
	for _, folder := range *folders {
		s = strings.ReplaceAll(s, folder+WorkerBranchPathSegment, WorkerBranchPathSegment)
	}
	return s
}
