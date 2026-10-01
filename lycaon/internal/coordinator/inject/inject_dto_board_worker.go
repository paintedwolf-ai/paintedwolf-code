package inject

import (
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/pkg/api"
)

// BoardOrientationInjectSentinel is embedded in inject/board-orientation.md for dedup policy.
const BoardOrientationInjectSentinel = "<!-- lycaon-board-orientation:v1 -->"

// WorkerLegInjectSentinel is embedded in inject/worker-leg.md for dedup policy.
const WorkerLegInjectSentinel = "<!-- lycaon-worker-leg:v1 -->"

// ChecklistItemView is one numbered playbook row for worker-leg inject.
type ChecklistItemView struct {
	Index int
	Text  string
}

// BoardLineView is one orientation line for pongo render.
type BoardLineView struct {
	Text string
}

// SiblingNoteView is one peer record_finding for worker-leg inject.
type SiblingNoteView struct {
	ID      int64
	HasBody bool
	Agent   string
	Summary string
	Ref     string
}

// ReservedPathView is one sibling path reservation for worker-leg inject.
type ReservedPathView struct {
	Path     string
	JobID    string
	LegLabel string
}

// WorkerLegInjectData is the pongo data model for inject/worker-leg.md.
type WorkerLegInjectData struct {
	LegID              string
	AgentType          string
	WorkflowID         string
	Phase              string
	Topology           string
	RequiresIsolation  bool
	FailedLeaves       []string
	CompletionCriteria []string
	AllowedTools       []string
	Checklist          []ChecklistItemView
	ScanDigest         []string
	HasPeerFindings    bool
	SiblingNotesMore   bool
	SiblingNotesAfter  int64
	SiblingNotes       []SiblingNoteView
	ReservedPaths      []ReservedPathView
	LayoutTopLevel     []string
}

// BoardOrientationRootView is one root section for board-orientation inject.
type BoardOrientationRootView struct {
	Label     string
	IsPrimary bool
	Lines     []BoardLineView
	Truncated bool
}

// BoardInjectData is the pongo data model for inject/board-orientation.md.
type BoardInjectData struct {
	PackSentinel      string
	NowLine           string
	OmitDelegation    bool
	IncludeScanLegend bool
	RootCount         int
	MultiRoot         bool
	OrientationRoots  []BoardOrientationRootView
	RootOmittedCount  int
	Lines             []BoardLineView
}

const (
	boardMultiRootDataBudget = api.MaxBoardInjectChars / 5
	boardRootViewLimit       = 2
)

// BuildBoardInjectData maps snapshot facts into inject DTO fields.
func BuildBoardInjectData(snap api.BoardSnapshot, scope packboard.InjectScope, omitDelegation, includeScanLegend bool, now time.Time) BoardInjectData {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	multiRoot := len(snap.OrientationRoots) >= 2
	lineBudget := api.MaxBoardInjectChars
	if multiRoot && scope == packboard.InjectScopeFull {
		lineBudget = boardMultiRootDataBudget
	}
	orientLines, _ := packboard.FormatInjectBodyScoped(snap, scope, omitDelegation, now, lineBudget)
	nowLine := packboard.FormatNowLine(now)
	lines := make([]BoardLineView, 0, len(orientLines))
	for i, text := range orientLines {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(text, "Now: ") {
			nowLine = text
			continue
		}
		lines = append(lines, BoardLineView{Text: text})
	}
	rootCount := 1
	if multiRoot {
		rootCount = len(snap.OrientationRoots)
	}
	var orientationRoots []BoardOrientationRootView
	rootOmittedCount := 0
	if multiRoot && scope == packboard.InjectScopeFull {
		orientationRoots, rootOmittedCount = boardRootViews(snap.OrientationRoots)
	}
	return BoardInjectData{
		PackSentinel:      packboard.InjectSentinel(scope),
		NowLine:           nowLine,
		OmitDelegation:    omitDelegation,
		IncludeScanLegend: includeScanLegend,
		RootCount:         rootCount,
		MultiRoot:         multiRoot,
		OrientationRoots:  orientationRoots,
		RootOmittedCount:  rootOmittedCount,
		Lines:             lines,
	}
}

func boardRootViews(roots []api.BoardOrientationRoot) ([]BoardOrientationRootView, int) {
	count := min(len(roots), boardRootViewLimit)
	views := make([]BoardOrientationRootView, count)
	remaining := boardMultiRootDataBudget
	for i := range views {
		label := limitBoardText(strings.TrimSpace(roots[i].Label), 16)
		if label == "" {
			label = "folder"
		}
		views[i] = BoardOrientationRootView{Label: label, IsPrimary: roots[i].IsPrimary, Truncated: roots[i].Truncated}
		remaining -= len(label) + 12
	}
	for i := range views {
		lines := packboard.RepoOrientationLines(roots[i].Brief)
		view := &views[i]
		for _, text := range lines {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			if len(text)+1 > remaining {
				view.Truncated = true
				break
			}
			view.Lines = append(view.Lines, BoardLineView{Text: text})
			remaining -= len(text) + 1
		}
	}
	return views, len(roots) - count
}

func limitBoardText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	end := 0
	for i := range text {
		if i > max {
			break
		}
		end = i
	}
	return strings.TrimSpace(text[:end])
}

// BoardInjectToMap converts the DTO for pongo2 render.
func BoardInjectToMap(data BoardInjectData) map[string]any {
	lines := make([]map[string]any, len(data.Lines))
	for i, line := range data.Lines {
		lines[i] = map[string]any{"text": line.Text}
	}
	orientationRoots := make([]map[string]any, len(data.OrientationRoots))
	for i, root := range data.OrientationRoots {
		rootLines := make([]map[string]any, len(root.Lines))
		for j, line := range root.Lines {
			rootLines[j] = map[string]any{"text": line.Text}
		}
		orientationRoots[i] = map[string]any{
			"label":      root.Label,
			"is_primary": root.IsPrimary,
			"lines":      rootLines,
			"truncated":  root.Truncated,
		}
	}
	return map[string]any{
		"pack_sentinel":       data.PackSentinel,
		"now_line":            data.NowLine,
		"omit_delegation":     data.OmitDelegation,
		"include_scan_legend": data.IncludeScanLegend,
		"root_count":          data.RootCount,
		"multi_root":          data.MultiRoot,
		"orientation_roots":   orientationRoots,
		"root_omitted_count":  data.RootOmittedCount,
		"lines":               lines,
	}
}

// BuildWorkerLegInjectData maps worker leg context into inject DTO fields.
func BuildWorkerLegInjectData(ctx WorkerLegContext) WorkerLegInjectData {
	ctx = BuildWorkerPromptContext(ctx)
	checklist := make([]ChecklistItemView, 0, len(ctx.Checklist))
	for i, item := range ctx.Checklist {
		text := strings.TrimSpace(item)
		if text == "" {
			continue
		}
		checklist = append(checklist, ChecklistItemView{Index: i + 1, Text: text})
	}
	siblingNotes := make([]SiblingNoteView, 0, len(ctx.SiblingNotes))
	for _, note := range ctx.SiblingNotes {
		summary := strings.TrimSpace(note.Summary)
		if summary == "" {
			continue
		}
		siblingNotes = append(siblingNotes, SiblingNoteView{
			ID: note.ID, HasBody: note.HasBody,
			Agent:   strings.TrimSpace(note.Agent),
			Summary: summary,
			Ref:     strings.TrimSpace(note.Ref),
		})
	}
	reservedPaths := make([]ReservedPathView, 0, len(ctx.ReservedPaths))
	for _, hold := range ctx.ReservedPaths {
		path := strings.TrimSpace(hold.Path)
		if path == "" {
			continue
		}
		reservedPaths = append(reservedPaths, ReservedPathView{
			Path:     path,
			JobID:    strings.TrimSpace(hold.JobID),
			LegLabel: strings.TrimSpace(hold.LegLabel),
		})
	}
	return WorkerLegInjectData{
		LegID:              strings.TrimSpace(ctx.LegID),
		AgentType:          strings.TrimSpace(ctx.AgentType),
		WorkflowID:         strings.TrimSpace(ctx.WorkflowID),
		Phase:              strings.TrimSpace(ctx.PhaseID),
		Topology:           strings.TrimSpace(ctx.TopologyPattern),
		RequiresIsolation:  ctx.RequiresIsolation,
		FailedLeaves:       append([]string(nil), ctx.FailedLeaves...),
		CompletionCriteria: append([]string(nil), ctx.CompletionCriteria...),
		AllowedTools:       append([]string(nil), ctx.LegTools...),
		Checklist:          checklist,
		ScanDigest:         append([]string(nil), ctx.ScanDigest...),
		HasPeerFindings:    ctx.HasPeerFindings || len(ctx.SiblingNotes) > 0,
		SiblingNotes:       siblingNotes,
		SiblingNotesMore:   ctx.SiblingNotesMore,
		SiblingNotesAfter:  ctx.SiblingNotesAfter,
		ReservedPaths:      reservedPaths,
		LayoutTopLevel:     append([]string(nil), ctx.LayoutTopLevel...),
	}
}

// WorkerLegInjectToMap converts the DTO for pongo2 render.
func WorkerLegInjectToMap(data WorkerLegInjectData) map[string]any {
	partial := []string{}
	checklistRows, omitted := boundWorkerRows(data.Checklist)
	if omitted > 0 {
		partial = append(partial, workerOmission("checklist", omitted))
	}
	checklist := make([]map[string]any, len(checklistRows))
	for i, item := range checklistRows {
		checklist[i] = map[string]any{
			"index": item.Index,
			"text":  item.Text,
		}
	}
	siblingRows, omitted := boundWorkerRows(data.SiblingNotes)
	if omitted > 0 {
		partial = append(partial, workerOmission("peer findings", omitted))
	}
	siblingNotes := make([]map[string]any, len(siblingRows))
	for i, note := range siblingRows {
		siblingNotes[i] = map[string]any{
			"id": note.ID, "has_body": note.HasBody,
			"agent":   note.Agent,
			"summary": note.Summary,
			"ref":     note.Ref,
		}
	}
	reservedRows, omitted := boundWorkerRows(data.ReservedPaths)
	if omitted > 0 {
		partial = append(partial, workerOmission("reserved paths", omitted))
	}
	reservedPaths := make([]map[string]any, len(reservedRows))
	for i, hold := range reservedRows {
		reservedPaths[i] = map[string]any{
			"path":      hold.Path,
			"job_id":    hold.JobID,
			"leg_label": hold.LegLabel,
		}
	}
	failedLeaves, omitted := boundWorkerRows(data.FailedLeaves)
	if omitted > 0 {
		partial = append(partial, workerOmission("failed leaves", omitted))
	}
	completionCriteria, omitted := boundWorkerRows(data.CompletionCriteria)
	if omitted > 0 {
		partial = append(partial, workerOmission("completion criteria", omitted))
	}
	allowedTools, omitted := boundWorkerRows(data.AllowedTools)
	if omitted > 0 {
		partial = append(partial, workerOmission("allowed tools", omitted))
	}
	scanDigest, omitted := boundWorkerRows(data.ScanDigest)
	if omitted > 0 {
		partial = append(partial, workerOmission("scan digest", omitted))
	}
	layoutTopLevel, omitted := boundWorkerRows(data.LayoutTopLevel)
	if omitted > 0 {
		partial = append(partial, workerOmission("layout entries", omitted))
	}
	return map[string]any{
		"leg_id":              data.LegID,
		"agent_type":          data.AgentType,
		"workflow_id":         data.WorkflowID,
		"phase":               data.Phase,
		"topology":            data.Topology,
		"requires_isolation":  data.RequiresIsolation,
		"failed_leaves":       failedLeaves,
		"completion_criteria": completionCriteria,
		"allowed_tools":       allowedTools,
		"checklist":           checklist,
		"scan_digest":         scanDigest,
		"has_peer_findings":   data.HasPeerFindings,
		"sibling_notes_more":  data.SiblingNotesMore,
		"sibling_notes_after": data.SiblingNotesAfter,
		"sibling_notes":       siblingNotes,
		"reserved_paths":      reservedPaths,
		"layout_top_level":    layoutTopLevel,
		"partial_collections": partial,
	}
}

func boundWorkerRows[T any](rows []T) ([]T, int) {
	if len(rows) <= pongoplain.MaxCollectionItems {
		return rows, 0
	}
	return rows[:pongoplain.MaxCollectionItems], len(rows) - pongoplain.MaxCollectionItems
}

func workerOmission(name string, count int) string {
	return name + " (" + strconv.Itoa(count) + " omitted)"
}
