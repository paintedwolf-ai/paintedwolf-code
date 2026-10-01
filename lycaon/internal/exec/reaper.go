package exec

// ReaperCommand is the argument that runs the engine binary as the companion
// started by StartReaper.
const ReaperCommand = "internal-process-reaper"

// Reaper protocol lines are "<op><kind><id>\n": op '+' tracks and '-' forgets,
// kind 'g' names a process group and 'p' a single process.
const (
	reaperGroup   byte = 'g'
	reaperProcess byte = 'p'
)

// TrackProcessGroup records a started process group the engine owns, by its
// leader's pid, so the group cannot outlive the engine. The returned func
// forgets it once the caller has torn the group down.
func TrackProcessGroup(leader int) (untrack func()) { return track(reaperGroup, leader) }

// TrackProcess records a started child that shares the engine's process group,
// so that one process cannot outlive the engine. The returned func forgets it
// once the child has exited.
func TrackProcess(pid int) (untrack func()) { return track(reaperProcess, pid) }
