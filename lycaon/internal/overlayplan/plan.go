package overlayplan

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/idset"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// PreviewSnapshot is overlay-level fields from the latest preview_overlay / promote_overlay.
type PreviewSnapshot struct {
	PromoteOrder  api.WorkerPromoteOrderKind
	PromoteAfter  []string
	BlockedBy     []string
	CleanPaths    []string
	ConflictPaths []string
}

// Build assembles a session-level promote sequence and overlap graph for pending overlays.
func Build(
	sessionID string,
	tasks []api.WorkerTask,
	preview func(sessionID, jobID string) (PreviewSnapshot, bool),
) api.OverlayMergePlan {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return api.OverlayMergePlan{}
	}
	var pending []api.WorkerTask
	for _, task := range tasks {
		if strings.TrimSpace(task.ParentSessionID) != sessionID {
			continue
		}
		if !api.WorkerTaskOverlayOpen(&task) {
			continue
		}
		pending = append(pending, task)
	}
	if len(pending) == 0 {
		return api.OverlayMergePlan{}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })

	pendingSet := map[string]struct{}{}
	pathToJobs := map[string][]string{}
	entries := make([]api.OverlayMergePlanEntry, 0, len(pending))

	for _, task := range pending {
		pendingSet[task.ID] = struct{}{}
		paths := planPaths(task)
		for _, p := range paths {
			pathToJobs[p] = append(pathToJobs[p], task.ID)
		}
		entry := api.OverlayMergePlanEntry{
			WorkerID:     task.ID,
			AgentType:    strings.TrimSpace(task.AgentType),
			ChangedPaths: paths,
		}
		if snap, ok := preview(sessionID, task.ID); ok {
			entry.PromoteOrder = snap.PromoteOrder
			entry.PromoteAfter = append([]string(nil), snap.PromoteAfter...)
			entry.BlockedBy = append([]string(nil), snap.BlockedBy...)
			entry.ConflictPaths = append([]string(nil), snap.ConflictPaths...)
			entry.CleanPaths = append([]string(nil), snap.CleanPaths...)
		} else {
			entry.PreviewStale = true
		}
		entries = append(entries, entry)
	}

	for path := range pathToJobs {
		pathToJobs[path] = idset.UnionSorted(nil, pathToJobs[path])
	}

	var shared []api.OverlayMergeSharedPath
	for path, jobIDs := range pathToJobs {
		if len(jobIDs) < 2 {
			continue
		}
		shared = append(shared, api.OverlayMergeSharedPath{
			Path:      path,
			WorkerIDs: jobIDs,
		})
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].Path < shared[j].Path })

	sequence := promoteSequence(entries, pendingSet, shared)

	return api.OverlayMergePlan{
		PendingCount:    len(pending),
		PromoteSequence: sequence,
		SharedPaths:     shared,
		Entries:         entries,
	}
}

func planPaths(task api.WorkerTask) []string {
	if task.Result != nil && task.Result.ChangeReport != nil {
		if paths := normalizePlanPaths(task.Result.ChangeReport.ChangedPaths); len(paths) > 0 {
			return paths
		}
	}
	if len(task.TouchedPaths) > 0 {
		return normalizePlanPaths(task.TouchedPaths)
	}
	return nil
}

func normalizePlanPaths(paths []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range paths {
		p := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if p == "" || p == "." || sandbox.HasParentTraversal(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func promoteSequence(
	entries []api.OverlayMergePlanEntry,
	pending map[string]struct{},
	shared []api.OverlayMergeSharedPath,
) []string {
	if len(entries) == 0 {
		return nil
	}
	inDegree := map[string]int{}
	blockedBy := map[string][]string{}
	for id := range pending {
		inDegree[id] = 0
	}
	for _, entry := range entries {
		for _, sibling := range entry.BlockedBy {
			sibling = strings.TrimSpace(sibling)
			if sibling == "" {
				continue
			}
			if _, ok := pending[sibling]; !ok {
				continue
			}
			// blocked_by: promote this overlay before listed siblings land.
			blockedBy[sibling] = append(blockedBy[sibling], entry.WorkerID)
		}
		for _, after := range entry.PromoteAfter {
			after = strings.TrimSpace(after)
			if after == "" {
				continue
			}
			if _, ok := pending[after]; !ok {
				continue
			}
			// promote_after: this overlay becomes clean only after listed siblings promote.
			blockedBy[entry.WorkerID] = append(blockedBy[entry.WorkerID], after)
		}
	}
	for jobID, blockers := range blockedBy {
		seen := map[string]struct{}{}
		for _, b := range blockers {
			if _, ok := pending[b]; !ok {
				continue
			}
			if b == jobID {
				continue
			}
			if _, ok := seen[b]; ok {
				continue
			}
			seen[b] = struct{}{}
			inDegree[jobID]++
		}
		blockedBy[jobID] = idset.UnionSorted(nil, blockers)
	}

	if len(shared) > 0 {
		sharedCount := map[string]int{}
		for _, row := range shared {
			for _, id := range row.WorkerIDs {
				sharedCount[id]++
			}
		}
		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				a, b := entries[i], entries[j]
				if !entriesSharePath(a, b) {
					continue
				}
				if a.PreviewStale && b.PreviewStale {
					first, second := a.WorkerID, b.WorkerID
					if sharedCount[a.WorkerID] > sharedCount[b.WorkerID] {
						first, second = b.WorkerID, a.WorkerID
					} else if sharedCount[a.WorkerID] == sharedCount[b.WorkerID] && a.WorkerID > b.WorkerID {
						first, second = b.WorkerID, a.WorkerID
					}
					if !hasBlocker(blockedBy, second, first) {
						blockedBy[second] = append(blockedBy[second], first)
						inDegree[second]++
					}
				}
			}
		}
	}

	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	var sequence []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sequence = append(sequence, id)
		for blockedID, blockers := range blockedBy {
			for _, b := range blockers {
				if b != id {
					continue
				}
				inDegree[blockedID]--
				if inDegree[blockedID] == 0 {
					queue = append(queue, blockedID)
				}
				break
			}
		}
		sort.Strings(queue)
	}
	if len(sequence) < len(entries) {
		seen := map[string]struct{}{}
		for _, id := range sequence {
			seen[id] = struct{}{}
		}
		var rest []string
		for id := range pending {
			if _, ok := seen[id]; ok {
				continue
			}
			rest = append(rest, id)
		}
		sort.Slice(rest, func(i, j int) bool {
			if inDegree[rest[i]] != inDegree[rest[j]] {
				return inDegree[rest[i]] < inDegree[rest[j]]
			}
			return rest[i] < rest[j]
		})
		sequence = append(sequence, rest...)
	}
	return sequence
}

func entriesSharePath(a, b api.OverlayMergePlanEntry) bool {
	if len(a.ChangedPaths) == 0 || len(b.ChangedPaths) == 0 {
		return false
	}
	set := map[string]struct{}{}
	for _, p := range a.ChangedPaths {
		set[p] = struct{}{}
	}
	for _, p := range b.ChangedPaths {
		if _, ok := set[p]; ok {
			return true
		}
	}
	return false
}

func hasBlocker(blockedBy map[string][]string, jobID, blocker string) bool {
	for _, b := range blockedBy[jobID] {
		if b == blocker {
			return true
		}
	}
	return false
}
