package reporting_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/pkg/api"
)

func recentMemoryFindings(t *testing.T, store *findings.MemoryStore, sessionID string) []findings.Finding {
	t.Helper()
	rows, _, err := store.Recent(context.Background(), sessionID, "", 0, 10, time.Time{})
	testutil.FailErr(t, "recent findings", err)
	return rows
}

func TestRecordFindingAppendsToStore(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := findings.NewMemoryStore()
	dir := t.TempDir()
	scopeKey := testFindingsScopeKey(dir)
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, store, scopeKey))
	tctx := findingContext(dir)
	tctx.Identity.WorkerJobID = "job-7"
	tctx.Identity.Agent = "implementer"
	out, err := reg.Run(context.Background(), "record_finding", map[string]any{
		"summary": "config resolver lives in internal/config/resolve.go:40",
		"ref":     "internal/config/resolve.go:40",
	}, tctx)
	testutil.FailErr(t, "run record_finding", err)
	if !strings.Contains(out, "appended") {
		t.Fatalf("out = %q", out)
	}
	found := recentMemoryFindings(t, store, dir)
	if len(found) != 1 || found[0].Agent != "job-7" || found[0].Ref != "internal/config/resolve.go:40" {
		t.Fatalf("findings = %+v", found)
	}
}

func TestRecordFindingDuplicateIsIdempotent(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := findings.NewMemoryStore()
	dir := t.TempDir()
	scopeKey := testFindingsScopeKey(dir)
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, store, scopeKey))
	tctx := findingContext(dir)
	tctx.Identity.WorkerJobID = "job-7"
	args := map[string]any{
		"summary": "OverlayScrollbars wraps chat stream viewport",
		"ref":     "lycaon-den/src/platform/scrolling/themed-scrollbars.ts",
	}
	out, err := reg.Run(context.Background(), "record_finding", args, tctx)
	testutil.FailErr(t, "first record_finding", err)
	if !strings.Contains(out, `"status":"appended"`) {
		t.Fatalf("first out = %q", out)
	}
	out, err = reg.Run(context.Background(), "record_finding", args, tctx)
	testutil.FailErr(t, "duplicate record_finding", err)
	if !strings.Contains(out, `"status":"unchanged"`) {
		t.Fatalf("duplicate out = %q want unchanged", out)
	}
	found := recentMemoryFindings(t, store, dir)
	if len(found) != 1 {
		t.Fatalf("findings = %+v want 1 row", found)
	}
}

func TestRecordFindingRequiresSummary(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	dir := t.TempDir()
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findings.NewMemoryStore(), testFindingsScopeKey(dir)))
	_, err := reg.Run(context.Background(), "record_finding", map[string]any{"summary": "  "},
		findingContext(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "summary is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordFindingRequiresReference(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	dir := t.TempDir()
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findings.NewMemoryStore(), testFindingsScopeKey(dir)))
	_, err := reg.Run(context.Background(), "record_finding", map[string]any{"summary": "A useful observation"}, findingContext(dir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "FINDING_UNGROUNDED" {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordFindingRejectsOversizeSummary(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	dir := t.TempDir()
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findings.NewMemoryStore(), testFindingsScopeKey(dir)))
	_, err := reg.Run(context.Background(), "record_finding",
		map[string]any{"summary": strings.Repeat("x", findings.SummaryMaxChars+1)},
		findingContext(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "TOOL_ARGS_INVALID") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecordFindingGroundingBlockNotStored(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	gate := &fakeFindingGate{findingErr: &toolrejection.ToolReject{Code: "FINDING_UNGROUNDED"}}
	store := findings.NewMemoryStore()
	dir := t.TempDir()
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{Grounding: gate}, store, testFindingsScopeKey(dir)))
	tctx := findingContext(dir)
	tctx.Identity.SessionID = "s1"
	tctx.Identity.Agent = "implementer"
	_, err := reg.Run(context.Background(), "record_finding",
		map[string]any{"summary": "ungrounded claim about something"},
		tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "FINDING_UNGROUNDED" {
		t.Fatalf("err = %v", err)
	}
	if found := recentMemoryFindings(t, store, dir); len(found) != 0 {
		t.Fatalf("blocked finding was stored: %+v", found)
	}
}

type fakeFindingGate struct {
	findingErr error
}

func (g *fakeFindingGate) AuditFinding(context.Context, string, string, tools.ToolContext) error {
	return g.findingErr
}

func TestRecordFindingAppendUsesDelegationKeyNotOverlay(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := findings.NewMemoryStore()
	delegationDir := t.TempDir()
	overlay := filepath.Join(delegationDir, settingsoverlay.DirName(), "overlays", "job-a")
	scopeKey := func(_ context.Context, _ string) string { return delegationDir }
	testutil.FailErr(t, "register", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, store, scopeKey))
	tctx := findingContext(overlay)
	tctx.Identity.SessionID = "child-a"
	tctx.Identity.WorkerJobID = "job-a"
	_, err := reg.Run(context.Background(), "record_finding", map[string]any{
		"summary": "peer-visible note",
		"ref":     "pkg/foo.go",
	}, tctx)
	testutil.FailErr(t, "run record_finding", err)
	if found := recentMemoryFindings(t, store, delegationDir); len(found) != 1 {
		t.Fatalf("delegation findings = %+v want 1", found)
	}
	if found := recentMemoryFindings(t, store, overlay); len(found) != 0 {
		t.Fatalf("overlay findings = %+v want empty", found)
	}
}

func testFindingsScopeKey(dir string) reporttools.FindingsScopeKey {
	return func(_ context.Context, _ string) string { return dir }
}

func TestRegisterRecordFindingToolRequiresScopeKey(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	err := native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, findings.NewMemoryStore(), nil)
	if err == nil || !strings.Contains(err.Error(), "scope key required") {
		t.Fatalf("err = %v", err)
	}
}

func findingContext(dir string) tools.ToolContext {
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	// Publish a below-threshold count for open-root tool tests.
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID:        "r1",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject,
			RepoFileCount:       100,
			RepoFileCountKnown:  true},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID,
			SessionID: "test-session"},
	}
}

func TestRecordFindingUnicodeDetailAndCorrections(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	store := findings.NewMemoryStore()
	dir := t.TempDir()
	testutil.FailErr(t, "register findings", native.RegisterRecordFindingTool(reg, reporttools.RecordFindingGates{}, store, testFindingsScopeKey(dir)))
	ctx := findingContext(dir)
	for _, summary := range []string{strings.Repeat("界", 320), "corrected signature at the same reference"} {
		_, err := reg.Run(t.Context(), "record_finding", map[string]any{"summary": summary, "ref": "source.go:2", "body": "render(state: ViewState): void"}, ctx)
		testutil.FailErr(t, "record correction", err)
	}
	rows := recentMemoryFindings(t, store, dir)
	if len(rows) != 2 || rows[0].Body != "render(state: ViewState): void" {
		t.Fatalf("findings: %+v", rows)
	}
	_, err := reg.Run(t.Context(), "record_finding", map[string]any{"summary": "detail too large", "ref": "source.go:2", "body": strings.Repeat("界", 3000)}, ctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("oversize body error: %v", err)
	}
}
