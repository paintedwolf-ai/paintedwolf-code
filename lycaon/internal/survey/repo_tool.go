package survey

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	repoViewDigest             = "digest"
	surveyRepoMaxResponseBytes = 32_000
)

// RepoTool runs catalog survey bundles.
type RepoTool struct {
	Boundary      *sandbox.Boundary
	BaseCatalog   *Catalog
	Caps          Caps
	SourceCatalog *sourcecatalog.Catalog
}

type snapshotEntry struct {
	Handle string `json:"handle"`
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Body   string `json:"body,omitempty"`
}

type repoResponse struct {
	Bundle           string          `json:"bundle"`
	Path             string          `json:"path"`
	View             string          `json:"view,omitempty"`
	Digest           string          `json:"digest"`
	Selected         int             `json:"selected"`
	Total            int             `json:"total"`
	ProbesRun        int             `json:"probes_run"`
	Snapshot         []snapshotEntry `json:"snapshot,omitempty"`
	Truncated        bool            `json:"truncated,omitempty"`
	Altitude         string          `json:"altitude,omitempty"`
	Resolution       string          `json:"resolution,omitempty"`
	InventoryState   string          `json:"inventory_state,omitempty"`
	EntriesExamined  int             `json:"entries_examined,omitempty"`
	InventoryEntries int             `json:"inventory_entries,omitempty"`
	Groups           int             `json:"groups,omitempty"`
	GroupsFolded     int             `json:"groups_folded,omitempty"`
}

// Run executes survey_repo with bundle and optional path args.
func (t *RepoTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if t == nil || t.Boundary == nil || t.BaseCatalog == nil {
		return "", fmt.Errorf("survey_repo: not configured")
	}
	bundleID := strings.TrimSpace(argString(args, "bundle"))
	if bundleID == "" {
		return "", &tools.ToolReject{Code: "SURVEY_BUNDLE_REQUIRED", Data: map[string]any{}}
	}
	relPath := strings.TrimSpace(argString(args, "path"))
	if relPath == "" {
		relPath = "."
	}
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, relPath)
	if err != nil {
		return "", err
	}
	cat := t.BaseCatalog.Clone()
	projectRoot := strings.TrimSpace(tctx.ActiveRootPath())
	if projectRoot != "" {
		overlayDir := filepath.Join(projectRoot, settingsoverlay.DirName(), "survey")
		if err := MergeOverlay(cat, extpacks.OnDisk(overlayDir)); err != nil {
			return "", err
		}
	}
	if _, ok := cat.Bundles[bundleID]; !ok {
		return "", &tools.ToolReject{
			Code: "SURVEY_BUNDLE_UNKNOWN",
			Data: map[string]any{"bundle": bundleID, "available": cat.BundleIDs()},
		}
	}
	runner := NewRunner(cat, t.Caps)
	started := time.Now()
	profileID := strings.TrimSpace(tctx.Agent)
	if profileID == "" {
		profileID = tools.DefaultToolProfileID
	}
	readFilter, err := t.Boundary.CompileReadFilter(ctx, resolved.Root.Path, profileID)
	if err != nil {
		return "", err
	}
	result, err := runner.Run(ctx, bundleID, relPath, Scope{
		Boundary: t.Boundary, ToolCtx: tctx, SourceCatalog: t.SourceCatalog, ReadFilter: readFilter,
	})
	if err != nil {
		if errors.Is(err, ErrSurveyPathEscape) {
			return "", &tools.ToolReject{Code: "SURVEY_PATH_ESCAPE", Data: map[string]any{"path": relPath}}
		}
		return "", err
	}
	resp := buildRepoResponse(result)
	LogSurveyRepoCall(ctx, bundleID, resp.ProbesRun, resp.Total, result.Coverage, time.Since(started))
	out, err := capRepoResponse(&resp)
	if err == nil {
		recordRepoSources(tctx, resp)
	}
	return out, err
}

func buildRepoResponse(result *RunResult) repoResponse {
	total := len(result.Ledger.Handles)
	return repoResponse{
		Bundle:           result.BundleID,
		Path:             result.Path,
		View:             repoViewDigest,
		Digest:           result.Digest,
		Selected:         0,
		Total:            total,
		ProbesRun:        result.ProbesRun,
		Snapshot:         snapshotEntries(result.Ledger),
		Altitude:         result.Coverage.Altitude,
		Resolution:       result.Coverage.Resolution,
		InventoryState:   result.Coverage.InventoryState,
		EntriesExamined:  result.Coverage.EntriesExamined,
		InventoryEntries: result.Coverage.InventoryEntries,
		Groups:           result.Coverage.Groups,
		GroupsFolded:     result.Coverage.GroupsFolded,
	}
}

func snapshotEntries(ledger evidence.Ledger) []snapshotEntry {
	handles := evidence.HandlesSorted(ledger)
	out := make([]snapshotEntry, 0, len(handles))
	for _, handle := range handles {
		rec := ledger.Handles[handle]
		entry := snapshotEntry{
			Handle: handle,
			Kind:   rec.Kind,
			Path:   rec.Path,
		}
		if len(rec.LineRanges) > 0 {
			entry.Line = rec.LineRanges[0].Start
		}
		if len(rec.Body) > 0 {
			entry.Body = rec.Body[0]
		}
		out = append(out, entry)
	}
	return out
}

func capRepoResponse(resp *repoResponse) (string, error) {
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	if len(raw) <= surveyRepoMaxResponseBytes {
		return string(raw), nil
	}
	resp.Truncated = true
	all := resp.Snapshot
	low, high := 0, len(all)
	for low < high {
		mid := low + (high-low+1)/2
		resp.Snapshot = all[:mid]
		candidate, marshalErr := surveyjson.Marshal(resp)
		if marshalErr != nil {
			return "", marshalErr
		}
		if len(candidate) <= surveyRepoMaxResponseBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	resp.Snapshot = all[:low]
	out, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	if len(out) > surveyRepoMaxResponseBytes {
		return "", fmt.Errorf("survey_repo: digest exceeds %d-byte response limit", surveyRepoMaxResponseBytes)
	}
	return string(out), nil
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return v
}

func recordRepoSources(tctx tools.ToolContext, resp repoResponse) {
	for _, entry := range resp.Snapshot {
		switch entry.Kind {
		case "file", "grep", "read":
			tctx.RecordSourcePath(entry.Path, api.NavigationEntryKindFile)
		case "dir":
			tctx.RecordSourcePath(entry.Path, api.NavigationEntryKindFolder)
		}
	}
}
