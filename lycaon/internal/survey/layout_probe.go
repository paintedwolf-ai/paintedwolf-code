package survey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const layoutCatalogJoinBudget = 150 * time.Millisecond

// ProbeCoverage reports probe resolution and inventory state.
type ProbeCoverage struct {
	Altitude         string
	Resolution       string
	InventoryState   string
	EntriesExamined  int
	InventoryEntries int
	Groups           int
	GroupsFolded     int
	DrillPaths       []string
}

type layoutGroup struct {
	path       string
	isDir      bool
	files      int
	dirs       int
	bytes      int64
	extensions map[string]int
	samples    []string
}

const (
	layoutSampleCount    = 2
	layoutSampleMaxBytes = 120
)

func runLayoutProbe(
	ctx context.Context,
	probe Probe,
	relPath string,
	scope Scope,
	groupCap int,
) ([]evidence.Record, int, ProbeCoverage, error) {
	resolved, err := projectpaths.ResolveRead(ctx, scope.Boundary, scope.ToolCtx, relPath)
	if err != nil {
		return nil, 0, ProbeCoverage{}, err
	}
	root := resolved.Root
	readFilter := scope.ReadFilter
	if readFilter == nil && scope.Boundary != nil {
		profileID := strings.TrimSpace(scope.ToolCtx.Agent)
		if profileID == "" {
			profileID = tools.DefaultToolProfileID
		}
		readFilter, err = scope.Boundary.CompileReadFilter(ctx, root.Path, profileID)
		if err != nil {
			return nil, 0, ProbeCoverage{}, err
		}
	}
	base := normalizeLayoutPath(resolved.ScopeRel)
	displayBase := normalizeLayoutPath(resolved.DisplayPath)
	info, err := os.Stat(resolved.Abs)
	if err != nil {
		return nil, 0, ProbeCoverage{}, err
	}
	if !info.IsDir() {
		return layoutFileRecord(probe.Label, displayBase, info), 1, ProbeCoverage{
			Altitude: "file", Resolution: "file", InventoryState: "not_needed",
			EntriesExamined: 1, InventoryEntries: 1, Groups: 1,
		}, nil
	}
	catalog := scope.SourceCatalog
	if catalog == nil {
		catalog = sourcecatalog.Process()
	}
	roots := []sourcecatalog.Root{{ID: root.ID, Path: root.Path}}
	joinCtx, cancel := context.WithTimeout(ctx, layoutCatalogJoinBudget)
	snapshot, joinErr := catalog.Observe(joinCtx, scope.ToolCtx.ProjectID, roots)
	cancel()
	if joinErr != nil && ctx.Err() != nil {
		return nil, 0, ProbeCoverage{}, ctx.Err()
	}
	if joinErr != nil && !errors.Is(joinCtx.Err(), context.DeadlineExceeded) {
		return nil, 0, ProbeCoverage{}, joinErr
	}
	if joinErr == nil && layoutSnapshotCovers(snapshot, root.ID, base) {
		return layoutRecordsFromSnapshot(ctx, probe.Label, snapshot, root.ID, base, displayBase, readFilter, groupCap)
	}
	return shallowLayoutRecords(probe.Label, resolved.Abs, base, displayBase, snapshot.State, readFilter, groupCap)
}

func layoutFileRecord(label, rel string, info os.FileInfo) []evidence.Record {
	ext := strings.ToLower(filepath.Ext(rel))
	if ext == "" {
		ext = "(none)"
	}
	return []evidence.Record{{
		Kind: "file", Shape: evidence.ShapeFileRegion, SourceTool: "survey_probe", Survey: true,
		Path: rel, LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
		Body: []string{fmt.Sprintf("[%s] file bytes=%d ext=%s", label, info.Size(), ext)},
	}}
}

func layoutSnapshotCovers(snapshot sourcecatalog.Snapshot, rootID, base string) bool {
	if snapshot.State != sourcecatalog.StateReady {
		return false
	}
	if base == "." {
		for _, root := range snapshot.Roots {
			if root.ID == rootID {
				return true
			}
		}
		return false
	}
	_, ok := snapshot.Entry(rootID, base)
	return ok
}

func layoutRecordsFromSnapshot(
	ctx context.Context,
	label string,
	snapshot sourcecatalog.Snapshot,
	rootID, base, displayBase string,
	readFilter sandbox.ReadFilter,
	groupCap int,
) ([]evidence.Record, int, ProbeCoverage, error) {
	groups := map[string]*layoutGroup{}
	examined := 0
	err := snapshot.Walk(ctx, rootID, base, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if readFilter != nil && !readFilter(entry.Path, entry.IsDir) {
			return sourcecatalog.WalkSkip
		}
		examined++
		direct, ok := layoutDirectChild(entry.Path, base)
		if !ok {
			return sourcecatalog.WalkContinue
		}
		group := groups[direct]
		if group == nil {
			group = &layoutGroup{path: direct, extensions: map[string]int{}}
			groups[direct] = group
		}
		isDirect := entry.Path == direct
		if entry.IsDir || entry.TargetIsDir {
			group.dirs++
			if isDirect {
				group.isDir = true
			}
			return sourcecatalog.WalkContinue
		}
		group.files++
		group.bytes += entry.Size
		ext := strings.ToLower(filepath.Ext(entry.Path))
		if ext == "" {
			ext = "(none)"
		}
		group.extensions[ext]++
		if len(group.samples) < layoutSampleCount {
			group.samples = append(group.samples, compactLayoutSample(entry.Path))
		}
		return sourcecatalog.WalkContinue
	})
	if err != nil {
		return nil, 0, ProbeCoverage{}, err
	}
	ordered := displayLayoutGroups(orderedLayoutGroups(groups), base, displayBase)
	records, folded := renderLayoutGroups(label, displayBase, ordered, groupCap)
	coverage := ProbeCoverage{
		Altitude:         layoutAltitude(base),
		Resolution:       "subtree_rollups",
		InventoryState:   string(snapshot.State),
		EntriesExamined:  examined,
		InventoryEntries: len(snapshot.Entries),
		Groups:           len(ordered),
		GroupsFolded:     folded,
		DrillPaths:       layoutDrillPaths(ordered, 5),
	}
	return records, examined, coverage, nil
}

func shallowLayoutRecords(
	label, abs, base, displayBase string,
	state sourcecatalog.State,
	readFilter sandbox.ReadFilter,
	groupCap int,
) ([]evidence.Record, int, ProbeCoverage, error) {
	entries, err := sandbox.SurveyReadDir(abs, base, sandbox.SurveyOptions{
		Admit: func(rel, _ string, isDir bool) bool {
			return readFilter == nil || readFilter(rel, isDir)
		},
	})
	if err != nil {
		return nil, 0, ProbeCoverage{}, err
	}
	groups := make([]layoutGroup, 0, len(entries))
	for _, entry := range entries {
		group := layoutGroup{path: entry.Rel, isDir: entry.IsDir, extensions: map[string]int{}}
		if entry.IsDir {
			group.dirs = 1
		} else {
			group.files = 1
			if info, infoErr := entry.DirEntry.Info(); infoErr == nil {
				group.bytes = info.Size()
			}
			ext := strings.ToLower(filepath.Ext(entry.Rel))
			if ext == "" {
				ext = "(none)"
			}
			group.extensions[ext] = 1
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].isDir != groups[j].isDir {
			return groups[i].isDir
		}
		return groups[i].path < groups[j].path
	})
	groups = displayLayoutGroups(groups, base, displayBase)
	records, folded := renderLayoutGroups(label, displayBase, groups, groupCap)
	coverage := ProbeCoverage{
		Altitude:        layoutAltitude(base),
		Resolution:      "immediate_children",
		InventoryState:  string(state),
		EntriesExamined: len(entries),
		Groups:          len(groups),
		GroupsFolded:    folded,
		DrillPaths:      layoutDrillPaths(groups, 5),
	}
	return records, len(entries), coverage, nil
}

func displayLayoutGroups(groups []layoutGroup, base, displayBase string) []layoutGroup {
	if displayBase == base {
		return groups
	}
	for i := range groups {
		groups[i].path = displayLayoutPath(base, displayBase, groups[i].path)
		for j := range groups[i].samples {
			groups[i].samples[j] = displayLayoutPath(base, displayBase, groups[i].samples[j])
		}
	}
	return groups
}

func displayLayoutPath(base, displayBase, value string) string {
	if value == base {
		return displayBase
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(value, base), "/")
	if displayBase == "." {
		return rel
	}
	if rel == "" {
		return displayBase
	}
	return path.Join(displayBase, rel)
}

func orderedLayoutGroups(groups map[string]*layoutGroup) []layoutGroup {
	out := make([]layoutGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		if out[i].files != out[j].files {
			return out[i].files > out[j].files
		}
		if out[i].bytes != out[j].bytes {
			return out[i].bytes > out[j].bytes
		}
		return out[i].path < out[j].path
	})
	return out
}

func renderLayoutGroups(label, base string, groups []layoutGroup, cap int) ([]evidence.Record, int) {
	if cap <= 0 {
		cap = len(groups)
	}
	kept := groups
	var folded []layoutGroup
	if len(groups) > cap {
		if cap == 1 {
			kept = nil
			folded = groups
		} else {
			keep := cap - 1
			kept = groups[:keep]
			folded = groups[keep:]
		}
	}
	records := make([]evidence.Record, 0, len(kept)+1)
	if len(folded) > 0 {
		records = append(records, layoutOverviewRecord(label, base, groups, len(folded)))
	}
	for _, group := range kept {
		records = append(records, layoutGroupRecord(label, group))
	}
	return records, len(folded)
}

func layoutGroupRecord(label string, group layoutGroup) evidence.Record {
	kind := "file"
	body := fmt.Sprintf("[%s] file bytes=%d ext=%s", label, group.bytes, topLayoutExtensions(group.extensions, 5))
	if group.isDir {
		kind = "dir"
		body = fmt.Sprintf("[%s] dir files=%d dirs=%d bytes=%d ext=%s", label,
			group.files, max(group.dirs-1, 0), group.bytes, topLayoutExtensions(group.extensions, 5))
		if len(group.samples) > 0 {
			body += " sample=" + strings.Join(group.samples, ",")
		}
	}
	return evidence.Record{
		Kind: kind, Shape: evidence.ShapeFileRegion, SourceTool: "survey_probe", Survey: true,
		Path: group.path, LineRanges: []evidence.LineRange{{Start: 1, End: 1}}, Body: []string{body},
	}
}

func layoutOverviewRecord(label, base string, groups []layoutGroup, folded int) evidence.Record {
	files, dirs := 0, 0
	var bytes int64
	const sampleLimit = 8
	samples := make([]string, 0, sampleLimit)
	for _, group := range groups {
		files += group.files
		dirs += group.dirs
		bytes += group.bytes
		if len(samples) < sampleLimit {
			samples = append(samples, compactLayoutSample(group.path))
		}
	}
	body := fmt.Sprintf("[%s] overview groups=%d folded_groups=%d files=%d dirs=%d bytes=%d sample=%s",
		label, len(groups), folded, files, dirs, bytes, strings.Join(samples, ","))
	return evidence.Record{
		Kind: "rollup", Shape: evidence.ShapeFileRegion, SourceTool: "survey_probe", Survey: true,
		Path: base, LineRanges: []evidence.LineRange{{Start: 1, End: 1}}, Body: []string{body},
	}
}

func compactLayoutSample(value string) string {
	if len(value) <= layoutSampleMaxBytes {
		return value
	}
	const marker = "..."
	head := (layoutSampleMaxBytes - len(marker)) / 2
	tail := layoutSampleMaxBytes - len(marker) - head
	for head > 0 && !utf8.RuneStart(value[head]) {
		head--
	}
	tailStart := len(value) - tail
	for tailStart < len(value) && !utf8.RuneStart(value[tailStart]) {
		tailStart++
	}
	return value[:head] + marker + value[tailStart:]
}

func topLayoutExtensions(counts map[string]int, limit int) string {
	type pair struct {
		ext   string
		count int
	}
	ordered := make([]pair, 0, len(counts))
	for ext, count := range counts {
		ordered = append(ordered, pair{ext: ext, count: count})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].count != ordered[j].count {
			return ordered[i].count > ordered[j].count
		}
		return ordered[i].ext < ordered[j].ext
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	parts := make([]string, 0, len(ordered))
	for _, item := range ordered {
		parts = append(parts, fmt.Sprintf("%s:%d", item.ext, item.count))
	}
	return strings.Join(parts, ",")
}

func layoutDrillPaths(groups []layoutGroup, limit int) []string {
	out := make([]string, 0, limit)
	for _, group := range groups {
		if !group.isDir {
			continue
		}
		out = append(out, group.path)
		if len(out) == limit {
			break
		}
	}
	return out
}

func normalizeLayoutPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" || value == "." {
		return "."
	}
	return strings.TrimPrefix(path.Clean("/"+value), "/")
}

func layoutDirectChild(entryPath, base string) (string, bool) {
	rel := entryPath
	if base != "." {
		if entryPath == base {
			return "", false
		}
		rel = strings.TrimPrefix(entryPath, base+"/")
	}
	if rel == "" {
		return "", false
	}
	first := strings.SplitN(rel, "/", 2)[0]
	if base == "." {
		return first, true
	}
	return path.Join(base, first), true
}

func layoutAltitude(base string) string {
	if base == "." {
		return "root"
	}
	return "directory"
}
