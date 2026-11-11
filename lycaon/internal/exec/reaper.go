package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// ReaperCommand is the argument that runs the engine binary as the companion
// started by StartReaper.
const ReaperCommand = "internal-process-reaper"

// Reaper protocol lines are "<op><kind><payload>\n": op '+' tracks and '-'
// forgets; kind 'g' names a process group/session leader, 's' a command
// supervisor, 'p' a single process, 'm' a surviving session member transferred
// before leader reap, and 'd' a directory by absolute path.
const (
	reaperGroup      byte = 'g'
	reaperSupervisor byte = 's'
	reaperProcess    byte = 'p'
	reaperMember     byte = 'm'
	reaperDir        byte = 'd'
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
		if os.RemoveAll(path) == nil { // #nosec G703 -- a run directory the caller created with os.MkdirTemp
			untrack()
		}
	}
}

func trackPID(kind byte, id int) func() {
	if id <= 0 {
		return func() {}
	}
	if inc, ok := osprocess.IncarnationOf(id); ok {
		return trackIncarnation(kind, inc)
	}
	return track(kind, strconv.Itoa(id))
}

func trackIncarnation(kind byte, inc osprocess.Incarnation) func() {
	if inc.PID <= 0 {
		return func() {}
	}
	addPayload := strconv.Itoa(inc.PID)
	if inc.Start > 0 {
		addPayload = fmt.Sprintf("%d:%d", inc.PID, inc.Start)
	}
	return trackWithRemovePayload(kind, addPayload, strconv.Itoa(inc.PID))
}

func trackReaperMembers(leaderPID int, members []osprocess.Incarnation) {
	if leaderPID <= 0 {
		return
	}
	for _, member := range members {
		if member.PID > 0 && member.Start > 0 {
			_ = sendReaper('+', reaperMember, fmt.Sprintf("%d:%d:%d", leaderPID, member.PID, member.Start))
		}
	}
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
