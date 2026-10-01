package summarize

import "fmt"

func emitIndexedRemainder(parent *SubtreeNode, task string, caps Caps, budget int, pack *ContextPack, stats *CuratorStats, spent int, next []NextAction) (int, []NextAction) {
	r := parent.Remainder
	n := &SubtreeNode{Path: fmt.Sprintf("+%d more", r.Children), Kind: SubtreeKindDir, RollupOnly: true, Material: r.Material}
	row := rollupRow(n, nil)
	cost := caps.EstimateTokens(rollupSkeletonLine(row))
	if spent+cost <= budget || stats.ChildrenRolledUp == 0 {
		pack.Skeleton = append(pack.Skeleton, row)
		spent += cost
		stats.BreadthAdmits++
	}
	action := drillNextAction(n, parent.Path)
	action.Task = task
	if r.Next != "" {
		action.Cursor = encodeCursor(parent.Revision, parent.CursorScope, r.Next)
	}
	if len(parent.ScopePaths) > 0 {
		action.Path = ""
		action.Paths = append([]string(nil), parent.ScopePaths...)
	}
	stats.ChildrenRolledUp += r.Children
	return spent, append(next, action)
}
