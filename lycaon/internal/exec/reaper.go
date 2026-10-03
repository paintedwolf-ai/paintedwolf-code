package exec

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReaperCommand is the argument that runs the engine binary as the companion
// started by StartReaper.
const ReaperCommand = "internal-process-reaper"

// Reaper protocol lines are "<op><kind><payload>\n": op '+' tracks and '-'
// forgets; kind 'g' names a process group and 'p' a single process by pid, and
// 'd' a directory by absolute path.
const (
	reaperGroup   byte = 'g'
	reaperProcess byte = 'p'
	reaperDir     byte = 'd'
)

// TrackProcessGroup records a started process group the engine owns, by its
// leader's pid, so the group cannot outlive the engine. The returned func
// forgets it once the caller has torn the group down.
func TrackProcessGroup(leader int) (untrack func()) { return trackPID(reaperGroup, leader) }

// TrackProcess records a started child that shares the engine's process group,
// so that one process cannot outlive the engine. The returned func forgets it
// once the child has exited.
func TrackProcess(pid int) (untrack func()) { return trackPID(reaperProcess, pid) }

// TrackScratchDir records a scratch directory the engine owns, so the reaper
// removes it if the engine exits first. The returned func removes the
// directory and forgets it; a failed removal stays with the reaper.
func TrackScratchDir(path string) (remove func()) {
	untrack := func() {}
	if reaperDirPath(path) {
		untrack = track(reaperDir, path)
	}
	return func() {
		if os.RemoveAll(path) == nil {
			untrack()
		}
	}
}

func trackPID(kind byte, id int) func() {
	if id <= 0 {
		return func() {}
	}
	return track(kind, strconv.Itoa(id))
}

// reaperDirPath admits only paths the line protocol carries unambiguously and
// that lie strictly inside the temp root, so the reaper deletes nothing else.
func reaperDirPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\n\r") {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(os.TempDir()), path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
