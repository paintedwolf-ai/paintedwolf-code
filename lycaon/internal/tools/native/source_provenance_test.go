package native

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// fakeSourceLedger satisfies sourceledger.Recorder plus the provenance read
// surface, so tests exercise the tool plumbing without a database.
type fakeSourceLedger struct {
	heads        map[string]sourceledger.BranchHead
	headErr      error
	effects      map[string][]sourceledger.Effect
	attribution  map[string]sourceledger.AttributionResult
	authored     map[string][]string
	floor        int64
	floorFound   bool
	versions     map[string]sourceledger.RestorableVersion
	fileVersions map[string][]sourceledger.Version
	comparisons  map[string]sourceledger.Comparison
}

func (f *fakeSourceLedger) Record(context.Context, sourceledger.RecordInput) error { return nil }
func (f *fakeSourceLedger) RecordTx(context.Context, *sql.Tx, sourceledger.RecordInput) error {
	return nil
}

func (f *fakeSourceLedger) ReadRestorableVersion(_ context.Context, projectID, versionID string) (sourceledger.RestorableVersion, error) {
	if f.versions == nil {
		return sourceledger.RestorableVersion{}, sourceledger.ErrHistoryNotFound
	}
	ver, ok := f.versions[versionID]
	if !ok {
		return sourceledger.RestorableVersion{}, sourceledger.ErrHistoryNotFound
	}
	if ver.ProjectID != "" && ver.ProjectID != projectID {
		return sourceledger.RestorableVersion{}, sourceledger.ErrHistoryNotFound
	}
	return ver, nil
}

func (f *fakeSourceLedger) CompareVersions(_ context.Context, projectID, versionID string) (sourceledger.Comparison, error) {
	if f.comparisons != nil {
		if comp, ok := f.comparisons[versionID]; ok {
			return comp, nil
		}
	}
	return sourceledger.Comparison{}, sourceledger.ErrHistoryNotFound
}

func (f *fakeSourceLedger) CompareVersionPair(_ context.Context, projectID, beforeID, afterID string) (sourceledger.Comparison, error) {
	if f.comparisons != nil {
		if comp, ok := f.comparisons[beforeID+"->"+afterID]; ok {
			return comp, nil
		}
		if comp, ok := f.comparisons[afterID+"->"+beforeID]; ok {
			return sourceledger.Comparison{Before: comp.After, After: comp.Before}, nil
		}
		if comp, ok := f.comparisons[afterID]; ok {
			return comp, nil
		}
		if beforeID == afterID {
			side := sourceledger.ComparisonSide{VersionID: beforeID, Path: "main.go"}
			return sourceledger.Comparison{Before: side, After: side}, nil
		}
	}
	return sourceledger.Comparison{}, sourceledger.ErrHistoryNotFound
}

func (f *fakeSourceLedger) ResolveHead(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string) (sourceledger.BranchHead, error) {
	if f.headErr != nil {
		return sourceledger.BranchHead{}, f.headErr
	}
	head, ok := f.heads[rootID+"/"+path]
	if !ok {
		return sourceledger.BranchHead{}, sourceledger.ErrHistoryNotFound
	}
	return head, nil
}

func (f *fakeSourceLedger) LatestFileEffect(_ context.Context, _, fileID string) (sourceledger.Effect, bool, error) {
	effects := f.Effects[fileID]
	if len(effects) == 0 {
		return sourceledger.Effect{}, false, nil
	}
	return effects[0], true, nil
}

func (f *fakeSourceLedger) SessionActivityFloor(context.Context, string, string) (int64, bool, error) {
	return f.floor, f.floorFound, nil
}

func (f *fakeSourceLedger) GitTransitionsByIDs(context.Context, []string) (map[string]sourceledger.GitTransition, error) {
	return nil, nil
}

func (f *fakeSourceLedger) QueryFileEffects(_ context.Context, _, fileID string, afterOrdinal, _ int64, limit int) (sourceledger.FileEffectsResult, error) {
	out := sourceledger.FileEffectsResult{}
	for _, effect := range f.Effects[fileID] {
		if effect.Ordinal <= afterOrdinal || len(out.Effects) >= limit {
			continue
		}
		out.Effects = append(out.Effects, effect)
	}
	return out, nil
}

func (f *fakeSourceLedger) QueryAttribution(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string) (sourceledger.AttributionResult, error) {
	return f.attribution[rootID+"/"+path], nil
}

func (f *fakeSourceLedger) QueryFileVersions(_ context.Context, _, fileID string, limit int, _ int64) (sourceledger.FileVersionsResult, error) {
	versions := f.fileVersions[fileID]
	if len(versions) > limit {
		versions = versions[:limit]
	}
	return sourceledger.FileVersionsResult{FileID: fileID, Versions: versions}, nil
}

func (f *fakeSourceLedger) SessionAuthoredPaths(_ context.Context, _, _, rootID string) ([]string, error) {
	return f.authored[rootID], nil
}

func writeProvenanceFile(t *testing.T, dir, name, content string) (abs, sha string) {
	t.Helper()
	abs = filepath.Join(dir, name)
	testutil.FailErr(t, "write fixture", os.WriteFile(abs, []byte(content), 0o644))
	return abs, textfile.SHA256([]byte(content))
}

func provenanceCtx(dir string, ledger *fakeSourceLedger) tools.ToolContext {
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p1"
	tctx.Source.SourceLedger = ledger
	tctx.Source.History = tools.SourceHistory{Files: ledger, Comparison: ledger, Git: ledger, Authorship: ledger}
	return tctx
}

func receiptSource(t *testing.T, out string) map[string]any {
	t.Helper()
	var payload struct {
		Receipt struct {
			Source map[string]any `json:"source"`
		} `json:"receipt"`
	}
	testutil.FailErr(t, "decode receipt", json.Unmarshal([]byte(out), &payload))
	return payload.Receipt.Source
}

func TestReadReceiptStatesRecordedSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v9", SHA256: sha, State: "content"},
		},
		effects: map[string][]sourceledger.Effect{
			"f1": {{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 4, TS: time.Date(2026, 8, 26, 14, 2, 0, 0, time.UTC)}},
		},
	}
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "main.go"}, provenanceCtx(dir, ledger))
	testutil.FailErr(t, "read", err)
	source := receiptSource(t, out)
	if source["navigation"] != "source://r1/main.go" {
		t.Fatalf("navigation = %v", source["navigation"])
	}
	if source["recorded"] != true || source["version_id"] != "v9" {
		t.Fatalf("source stamp = %v, want recorded v9", source)
	}
	change, _ := source["last_change"].(map[string]any)
	if change["actor"] != "user" || change["at"] != "2026-08-26 14:02 UTC" {
		t.Fatalf("last_change = %v, want user at 14:02 UTC", change)
	}
}

func TestReadReceiptStatesUnrecordedBytesAsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v9", SHA256: "different", State: "content"},
		},
	}
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "main.go"}, provenanceCtx(dir, ledger))
	testutil.FailErr(t, "read", err)
	source := receiptSource(t, out)
	if source["recorded"] != false || source["note"] == nil || source["version_id"] != nil {
		t.Fatalf("unrecorded stamp = %v, want recorded=false with note and no version", source)
	}
}

func TestMixedPublicationRemainsVisibleToPublishingAgent(t *testing.T) {
	dir := t.TempDir()
	abs, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		floor: 1, floorFound: true,
		heads: map[string]sourceledger.BranchHead{"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"}},
		effects: map[string][]sourceledger.Effect{"f1": {{
			Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Turn: 9, Ordinal: 2,
			Contributors: []sourceledger.Contributor{
				{Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Turn: 9},
				{Origin: api.SourceChangeOriginUser, SessionID: "test-session", Turn: 9},
			},
		}}},
	}
	tctx := provenanceCtx(dir, ledger)
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(t.Context(), map[string]any{"path": "main.go"}, tctx)
	testutil.FailErr(t, "read mixed publication", err)
	change, _ := receiptSource(t, out)["last_change"].(map[string]any)
	if change["actor"] != "mixed" || change["detail"] != "you; user" {
		t.Fatalf("mixed receipt lost authors: %v", change)
	}
	changes, total, ok := sourceview.ForeignChanges(t.Context(), tctx, abs)
	if !ok || total != 1 || len(changes) != 1 || changes[0].Actor != "mixed" || changes[0].Turn != 0 {
		t.Fatalf("publisher hid coauthored changes: %+v, total=%d, ok=%v", changes, total, ok)
	}
}

func TestReadReceiptUntrackedPathStatesNoHistory(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	out, err := read.Run(context.Background(), map[string]any{"path": "main.go"}, provenanceCtx(dir, &fakeSourceLedger{}))
	testutil.FailErr(t, "read", err)
	source := receiptSource(t, out)
	if source["recorded"] != false || source["note"] == nil {
		t.Fatalf("untracked stamp = %v, want recorded=false with note", source)
	}
}

func TestReadReceiptWithoutLedgerMakesNoClaim(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p1"
	out, err := read.Run(context.Background(), map[string]any{"path": "main.go"}, tctx)
	testutil.FailErr(t, "read", err)
	if source := receiptSource(t, out); source != nil {
		t.Fatalf("no-ledger read stamped %v, want absent", source)
	}
}

func TestEditMissWithForeignChangeStatesProvenance(t *testing.T) {
	dir := t.TempDir()
	_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		floor: 1, floorFound: true,
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"},
		},
		effects: map[string][]sourceledger.Effect{
			"f1": {
				{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 8, TS: time.Date(2026, 8, 26, 14, 2, 0, 0, time.UTC)},
				{Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Op: api.SourceChangeOpWrite, Ordinal: 5},
			},
		},
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "main.go", "old_string": "package moved", "new_string": "package main",
	}, provenanceCtx(dir, ledger))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EDIT_TARGET_CHANGED_BY_OTHERS" {
		t.Fatalf("edit miss = %v, want EDIT_TARGET_CHANGED_BY_OTHERS", err)
	}
	rows, _ := reject.Data["changed_by"].([]map[string]any)
	if len(rows) != 1 || rows[0]["actor"] != "user" {
		t.Fatalf("changed_by = %v, want one user row (own effects excluded)", reject.Data["changed_by"])
	}
	if reject.Data["change_count"] != 1 {
		t.Fatalf("change_count = %v, want 1", reject.Data["change_count"])
	}
}

func TestEditMissWithOnlyOwnChangesKeepsPlainReject(t *testing.T) {
	dir := t.TempDir()
	_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		floor: 1, floorFound: true,
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"},
		},
		effects: map[string][]sourceledger.Effect{
			"f1": {{Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Op: api.SourceChangeOpWrite, Ordinal: 5}},
		},
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "main.go", "old_string": "package moved", "new_string": "package main",
	}, provenanceCtx(dir, ledger))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_NOT_FOUND" {
		t.Fatalf("edit miss = %v, want plain EDIT_OLD_STRING_NOT_FOUND", err)
	}
}

func TestEditMissWithoutRecordedFloorKeepsPlainReject(t *testing.T) {
	dir := t.TempDir()
	_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		floorFound: false,
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"},
		},
		effects: map[string][]sourceledger.Effect{
			"f1": {{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 8}},
		},
	}
	edit := &EditTool{Boundary: nativefixture.Boundary(t)}
	_, err := edit.Run(context.Background(), map[string]any{
		"path": "main.go", "old_string": "package moved", "new_string": "package main",
	}, provenanceCtx(dir, ledger))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "EDIT_OLD_STRING_NOT_FOUND" {
		t.Fatalf("edit miss without floor = %v, want plain reject — an unknown baseline states nothing", err)
	}
}

func TestReadSourceStampPreservesWorkerDestination(t *testing.T) {
	dir := t.TempDir()
	abs, sha := writeProvenanceFile(t, dir, "a #b.go", "file")
	ctx := provenanceCtx(dir, &fakeSourceLedger{})
	ctx.Identity.WorkerJobID = "worker-1"
	ctx.Source.WorkerBranchRoot = dir
	ctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	stamp := sourceview.ReadStamp(t.Context(), ctx, abs, sha)
	if stamp == nil || stamp.Navigation != "source://r1/a%20%23b.go?job_id=worker-1" {
		t.Fatalf("worker source stamp=%+v", stamp)
	}
}

func (f *fakeSourceLedger) ResolveHeadByFile(context.Context, string, sourcebranch.ID, string) (sourceledger.BranchHead, error) {
	return sourceledger.BranchHead{}, sourceledger.ErrHistoryNotFound
}
func (f *fakeSourceLedger) TurnCheckpoint(context.Context, string, string, int) (sourceledger.Checkpoint, bool, error) {
	return sourceledger.Checkpoint{}, false, nil
}
func (f *fakeSourceLedger) EffectsBetween(context.Context, string, int64, int64, int) ([]sourceledger.Effect, error) {
	return nil, nil
}
func (f *fakeSourceLedger) GitTransitionsBetween(context.Context, string, int64, int64, int) ([]sourceledger.GitTransition, error) {
	return nil, nil
}
