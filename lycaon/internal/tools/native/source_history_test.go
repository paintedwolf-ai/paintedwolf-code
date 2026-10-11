package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

func decodeSourceHistory(t *testing.T, out string) map[string]any {
	t.Helper()
	var payload map[string]any
	testutil.FailErr(t, "decode source_history", json.Unmarshal([]byte(out), &payload))
	return payload
}

func TestSourceHistoryEffectsClassifiesActors(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v3", SHA256: "abc", State: "content"},
		},
		effects: map[string][]sourceledger.Effect{
			"f1": {
				{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 9, AfterVersionID: "v3", TS: time.Date(2026, 8, 26, 14, 2, 0, 0, time.UTC)},
				{Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Turn: 2, Op: api.SourceChangeOpWrite, Ordinal: 7, AfterVersionID: "v2"},
				{Origin: api.SourceChangeOriginAgent, SessionID: "other", ActorLabel: "refactor worker", Op: api.SourceChangeOpCreate, Ordinal: 4, AfterVersionID: "v1"},
				{Origin: api.SourceChangeOriginAgent, SessionID: "other", Op: api.SourceChangeOpWrite, Ordinal: 3, BranchID: "another-worker-job"},
			},
		},
	}
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "main.go"}, provenanceCtx(dir, ledger))
	testutil.FailErr(t, "source_history effects", err)
	payload := decodeSourceHistory(t, out)
	if payload["recorded"] != true {
		t.Fatalf("recorded = %v, want true", payload["recorded"])
	}
	effects, _ := payload["effects"].([]any)
	if len(effects) != 3 {
		t.Fatalf("effects = %d, want 3 (foreign-workspace effect excluded)", len(effects))
	}
	first := effects[0].(map[string]any)
	second := effects[1].(map[string]any)
	third := effects[2].(map[string]any)
	if first["actor"] != "user" || second["actor"] != "you" || third["actor"] != "agent" {
		t.Fatalf("actors = %v %v %v, want user/you/agent", first["actor"], second["actor"], third["actor"])
	}
	if third["detail"] != "refactor worker" {
		t.Fatalf("foreign agent detail = %v, want recorded label", third["detail"])
	}
	if second["turn"] != float64(2) {
		t.Fatalf("own effect turn = %v, want 2", second["turn"])
	}
}

func TestSourceHistoryUntrackedPathStatesUnknownNotUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": "main.go"}, provenanceCtx(dir, &fakeSourceLedger{}))
	testutil.FailErr(t, "source_history untracked", err)
	payload := decodeSourceHistory(t, out)
	if payload["recorded"] != false || payload["note"] == nil {
		t.Fatalf("untracked = %v, want recorded=false with a note", payload)
	}
}

func TestSourceHistoryLinesFiltersRangeAndClassifies(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		attribution: map[string]sourceledger.AttributionResult{
			"r1/main.go": {
				FileID: "f1", HeadSHA256: "abc",
				Intervals: []sourceledger.AttributionInterval{
					{StartLine: 1, EndLine: 4, Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Turn: 1},
					{StartLine: 10, EndLine: 14, Origin: api.SourceChangeOriginUser},
					{StartLine: 30, EndLine: 31, Origin: api.SourceChangeOriginAgent, SessionID: "other", JobID: "j2"},
				},
			},
		},
	}
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"path": "main.go", "mode": "lines", "start_line": 8, "end_line": 32,
	}, provenanceCtx(dir, ledger))
	testutil.FailErr(t, "source_history lines", err)
	payload := decodeSourceHistory(t, out)
	intervals, _ := payload["intervals"].([]any)
	if len(intervals) != 2 {
		t.Fatalf("intervals = %d, want 2 in range", len(intervals))
	}
	userRow := intervals[0].(map[string]any)
	agentRow := intervals[1].(map[string]any)
	if userRow["actor"] != "user" || agentRow["actor"] != "agent" || agentRow["detail"] != "worker job" {
		t.Fatalf("interval actors = %v / %v, want user and agent(worker job)", userRow, agentRow)
	}
}

func TestSourceHistoryMineQualifiesNonPrimaryRoots(t *testing.T) {
	dir := t.TempDir()
	ledger := &fakeSourceLedger{
		authored: map[string][]string{
			"r1": {"src/app.go"},
			"r2": {"guide.md"},
		},
	}
	tctx := provenanceCtx(dir, ledger)
	tctx.Source.Roots = append(tctx.Source.Roots, tctx.Source.Roots[0])
	tctx.Source.Roots[1].ID, tctx.Source.Roots[1].Label, tctx.Source.Roots[1].IsPrimary = "r2", "docs", false
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"mode": "mine"}, tctx)
	testutil.FailErr(t, "source_history mine", err)
	payload := decodeSourceHistory(t, out)
	paths, _ := payload["paths"].([]any)
	if len(paths) != 2 || paths[0] != "src/app.go" || paths[1] != "@docs/guide.md" {
		t.Fatalf("mine paths = %v, want [src/app.go @docs/guide.md]", paths)
	}
}

func TestSourceHistoryRejectsUnknownModeAndMissingLedger(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}

	_, err := tool.Run(context.Background(), map[string]any{"path": "main.go", "mode": "blame"}, provenanceCtx(dir, &fakeSourceLedger{}))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_HISTORY_MODE_INVALID" {
		t.Fatalf("unknown mode = %v, want SOURCE_HISTORY_MODE_INVALID", err)
	}

	noLedger := nativefixture.Context(dir)
	noLedger.Identity.ProjectID = "p1"
	_, err = tool.Run(context.Background(), map[string]any{"path": "main.go"}, noLedger)
	if !errors.As(err, &reject) || reject.Code != "SOURCE_HISTORY_UNAVAILABLE" {
		t.Fatalf("missing ledger = %v, want SOURCE_HISTORY_UNAVAILABLE", err)
	}
}

func TestSourceHistoryVersionReadsExactBytes(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	content := "package main\n\n// historical version 1\nfunc run() {}\n"
	ledger := &fakeSourceLedger{
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {
				ID:        "v1",
				ProjectID: "p1",
				RootID:    "r1",
				Path:      "main.go",
				State:     "content",
				SHA256:    "abcdef1234567890",
				Content:   []byte(content),
			},
		},
	}
	tctx := provenanceCtx(dir, ledger)
	tctx.Identity.ProjectID = "p1"
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}

	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"mode":       "version",
		"version_id": "v1",
		"offset":     2,
		"limit":      2,
	}, tctx)
	testutil.FailErr(t, "source_history version", err)
	payload := decodeSourceHistory(t, out)

	if payload["mode"] != "version" {
		t.Fatalf("mode = %v, want version", payload["mode"])
	}
	if payload["version_id"] != "v1" {
		t.Fatalf("version_id = %v, want v1", payload["version_id"])
	}
	if payload["total_lines"] != float64(4) {
		t.Fatalf("total_lines = %v, want 4", payload["total_lines"])
	}
	if payload["offset"] != float64(2) || payload["end_line"] != float64(3) {
		t.Fatalf("offset/endLine = %v/%v, want 2/3", payload["offset"], payload["end_line"])
	}
	if payload["truncated"] != true {
		t.Fatalf("truncated = %v, want true", payload["truncated"])
	}
	if payload["next_offset"] != float64(4) {
		t.Fatalf("next_offset = %v, want 4", payload["next_offset"])
	}
	wantContent := "     2: \n     3: // historical version 1"
	if payload["content"] != wantContent {
		t.Fatalf("content = %q, want %q", payload["content"], wantContent)
	}
}

func TestSourceHistoryVersionHandlesAbsentState(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	ledger := &fakeSourceLedger{
		versions: map[string]sourceledger.RestorableVersion{
			"v_del": {
				ID:        "v_del",
				ProjectID: "p1",
				RootID:    "r1",
				Path:      "main.go",
				State:     "absent",
			},
		},
	}
	tctx := provenanceCtx(dir, ledger)
	tctx.Identity.ProjectID = "p1"
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}

	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"mode":       "version",
		"version_id": "v_del",
	}, tctx)
	testutil.FailErr(t, "source_history version absent", err)
	payload := decodeSourceHistory(t, out)

	if payload["state"] != "absent" {
		t.Fatalf("state = %v, want absent", payload["state"])
	}
	if payload["content"] != nil {
		t.Fatalf("content = %v, want nil for absent state", payload["content"])
	}
}

func TestSourceHistoryVersionRejections(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n")
	writeProvenanceFile(t, dir, "other.go", "package other\n")
	ledger := &fakeSourceLedger{
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {
				ID:        "v1",
				ProjectID: "p1",
				RootID:    "r1",
				Path:      "main.go",
				State:     "content",
				Content:   []byte("package main\n"),
			},
		},
	}
	tctx := provenanceCtx(dir, ledger)
	tctx.Identity.ProjectID = "p1"
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}

	// 1. Missing version_id
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "main.go", "mode": "version",
	}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_REQUIRED" {
		t.Fatalf("missing version_id err = %v, want SOURCE_VERSION_REQUIRED", err)
	}

	// 2. Version not found
	_, err = tool.Run(context.Background(), map[string]any{
		"path": "main.go", "mode": "version", "version_id": "nonexistent",
	}, tctx)
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_NOT_FOUND" {
		t.Fatalf("not found err = %v, want SOURCE_VERSION_NOT_FOUND", err)
	}

	// 3. Path mismatch
	_, err = tool.Run(context.Background(), map[string]any{
		"path": "other.go", "mode": "version", "version_id": "v1",
	}, tctx)
	if !errors.As(err, &reject) || reject.Code != "SOURCE_VERSION_PATH_MISMATCH" {
		t.Fatalf("mismatch err = %v, want SOURCE_VERSION_PATH_MISMATCH", err)
	}
}

func TestSourceHistoryDiff(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v3", State: "content"},
		},
		comparisons: map[string]sourceledger.Comparison{
			"v2": {
				Before: sourceledger.ComparisonSide{
					VersionID: "v1",
					Path:      "main.go",
					Content:   "line 1\nline 2\n",
				},
				After: sourceledger.ComparisonSide{
					VersionID: "v2",
					Path:      "main.go",
					Content:   "line 1\nline 2 modified\n",
				},
			},
			"v1->v3": {
				Before: sourceledger.ComparisonSide{
					VersionID: "v1",
					Path:      "main.go",
					Content:   "line 1\nline 2\n",
				},
				After: sourceledger.ComparisonSide{
					VersionID: "v3",
					Path:      "main.go",
					Content:   "line 1\nline 2 modified\nline 3 added\n",
				},
			},
		},
	}
	tctx := provenanceCtx(dir, ledger)
	tctx.Identity.ProjectID = "p1"
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(t)}

	// 1. Diff against parent (omitting base_version_id)
	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"mode":       "diff",
		"version_id": "v2",
	}, tctx)
	testutil.FailErr(t, "source_history diff parent", err)
	payload := decodeSourceHistory(t, out)
	if payload["mode"] != "diff" {
		t.Fatalf("mode = %v, want diff", payload["mode"])
	}
	diffText, _ := payload["diff"].(string)
	if diffText == "" {
		t.Fatalf("diff is empty, want unified diff")
	}

	// 2. Diff between explicit versions with base_version_id="v1"
	out, err = tool.Run(context.Background(), map[string]any{
		"path":            "main.go",
		"mode":            "diff",
		"version_id":      "v3",
		"base_version_id": "v1",
	}, tctx)
	testutil.FailErr(t, "source_history diff pair", err)
	payload = decodeSourceHistory(t, out)
	diffText, _ = payload["diff"].(string)
	if diffText == "" {
		t.Fatalf("diff pair is empty, want unified diff")
	}

	// 3. Diff against head with base_version_id="head"
	out, err = tool.Run(context.Background(), map[string]any{
		"path":            "main.go",
		"mode":            "diff",
		"version_id":      "v3",
		"base_version_id": "head",
	}, tctx)
	testutil.FailErr(t, "source_history diff head", err)
	payload = decodeSourceHistory(t, out)
	if payload["version_id"] != "v3" {
		t.Fatalf("version_id = %v, want v3", payload["version_id"])
	}
}
