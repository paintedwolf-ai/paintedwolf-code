package native

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/indexwatch"
	"github.com/lycaon/lycaon/internal/tools"
)

// watchIndex captures the write root's Git index when a sandbox applies, so
// the result can report index changes the sandbox kept out of the worktree.
func watchIndex(tctx tools.ToolContext, confinement *confine.Confinement) indexwatch.Snapshot {
	if confinement == nil {
		return indexwatch.Snapshot{}
	}
	return indexwatch.Take(tools.HostWriteRoot(tctx))
}

// stampIndexWatch hands the capture to the result's facts, which release it.
func stampIndexWatch(tctx tools.ToolContext, snapshot indexwatch.Snapshot) {
	if tctx.Effects.Out == nil {
		snapshot.Release()
		return
	}
	tctx.Effects.Out.Facts.IndexWatch = snapshot
}
