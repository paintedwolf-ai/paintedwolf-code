package cadence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectignore"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// ledgerRoot returns the canonical root path, which is what scans record and reads look up.
func ledgerRoot(t *testing.T) string {
	t.Helper()
	root, err := scanbase.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonical root", err)
	return root
}

// ledgerCadence opens the trust gate so the project's overlay applies.
func ledgerCadence(store *scanbase.SQLStore) *Service {
	return &Service{
		Store: store,
		OverlayRootsApply: func(_ context.Context, rootPaths []string) []string {
			return rootPaths
		},
	}
}

// ledgerFixture sets up a project with one scanner series for the ledger join.
func ledgerFixture(t *testing.T, root, scanner, execution string) *scanbase.SQLStore {
	t.Helper()
	store := authorityTestStore(t)
	testutil.FailErr(t, "upsert series", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
		CanonicalPath:                   root,
		ScannerID:                       scanner,
		Categories:                      []api.ScanCategory{api.ScanCategorySAST},
		LastCompletedAt:                 time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		LastCoveredExecutionFingerprint: execution,
		UpdatedAt:                       time.Now().UTC(),
	}))
	return store
}

func ledgerScan(t *testing.T, store *scanbase.SQLStore, id, root, scanner string, target api.ScanTargetKind, coverage api.ScanCoverageStatus, findings ...api.SecurityFinding) api.CodeScan {
	t.Helper()
	job := authorityScan(id, "assessment-"+id, root, "snapshot-"+id, scanner, target, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), job, []string{"a.go"}, ""))
	completed := completeNextScan(t, store, findings...)
	completed.CoverageStatus = coverage
	return completed
}

func ledgerStates(t *testing.T, store *scanbase.SQLStore, root string) map[string]api.FindingLedgerState {
	t.Helper()
	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	out := make(map[string]api.FindingLedgerState, len(page.Entries))
	for _, entry := range page.Entries {
		out[entry.Finding.RuleID] = entry.State
	}
	return out
}

func TestLedgerRecordsOpenFixedAndReopened(t *testing.T) {
	root := ledgerRoot(t)
	execution := "execution-1"
	store := ledgerFixture(t, root, "sast", execution)

	kept := scanfindings.FixtureFinding("rule-kept", api.FindingLevelHigh, "kept", "a.go", 1)
	gone := scanfindings.FixtureFinding("rule-gone", api.FindingLevelLow, "gone", "a.go", 2)
	first := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, kept, gone)
	first.ExecutionFingerprint = execution
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{kept, gone}, nil, at))

	states := ledgerStates(t, store, root)
	if states["rule-kept"] != api.FindingLedgerOpen || states["rule-gone"] != api.FindingLedgerOpen {
		t.Fatalf("states after introduction = %+v, want both open", states)
	}

	second := ledgerScan(t, store, "scan-2", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, kept)
	second.ExecutionFingerprint = execution
	testutil.FailErr(t, "fix", store.RecordFindingEvents(t.Context(), &second, nil, []api.SecurityFinding{gone}, at.Add(time.Minute)))

	states = ledgerStates(t, store, root)
	if states["rule-gone"] != api.FindingLedgerFixed {
		t.Fatalf("state after a path scan re-read the file = %q, want fixed", states["rule-gone"])
	}

	third := ledgerScan(t, store, "scan-3", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, kept, gone)
	third.ExecutionFingerprint = execution
	testutil.FailErr(t, "reintroduce", store.RecordFindingEvents(t.Context(), &third, []api.SecurityFinding{gone}, nil, at.Add(2*time.Minute)))

	states = ledgerStates(t, store, root)
	if states["rule-gone"] != api.FindingLedgerReopened {
		t.Fatalf("state after the finding came back = %q, want reopened", states["rule-gone"])
	}

	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	for _, entry := range page.Entries {
		if entry.Finding.RuleID != "rule-gone" {
			continue
		}
		if !entry.FirstSeenAt.Equal(at) {
			t.Fatalf("first_seen = %s, want the original introduction at %s", entry.FirstSeenAt, at)
		}
		if entry.Observations != 3 {
			t.Fatalf("observations = %d, want 3", entry.Observations)
		}
	}
}

// A bounded full pass may not have read the file, so its absence is not a fix.
func TestLedgerBoundedFullPassLeavesNotObserved(t *testing.T) {
	root := ledgerRoot(t)
	execution := "execution-1"
	store := ledgerFixture(t, root, "sast", execution)

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	first := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, finding)
	first.ExecutionFingerprint = execution
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{finding}, nil, at))

	bounded := ledgerScan(t, store, "scan-2", root, "sast", api.ScanTargetFull, api.ScanCoverageBounded)
	bounded.ExecutionFingerprint = execution
	testutil.FailErr(t, "bounded pass", store.RecordFindingEvents(t.Context(), &bounded, nil, []api.SecurityFinding{finding}, at.Add(time.Minute)))

	states := ledgerStates(t, store, root)
	if states["rule-a"] != api.FindingLedgerNotObserved {
		t.Fatalf("state after a bounded full pass = %q, want not_observed", states["rule-a"])
	}
}

// A path scan's absence is a fix only if the scan itself established coverage.
func TestLedgerPartialPathScanLeavesNotObserved(t *testing.T) {
	root := ledgerRoot(t)
	execution := "execution-1"
	store := ledgerFixture(t, root, "sast", execution)

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	first := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	first.ExecutionFingerprint = execution
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{finding}, nil, at))

	moved := ledgerScan(t, store, "scan-2", root, "sast", api.ScanTargetPaths, api.ScanCoveragePartial)
	moved.ExecutionFingerprint = execution
	testutil.FailErr(t, "partial delta", store.RecordFindingEvents(t.Context(), &moved, nil, []api.SecurityFinding{finding}, at.Add(time.Minute)))

	states := ledgerStates(t, store, root)
	if states["rule-a"] != api.FindingLedgerNotObserved {
		t.Fatalf("state after a partial delta = %q, want not_observed", states["rule-a"])
	}
}

// A fix recorded under a since-changed execution identity is unverified.
func TestLedgerMovedExecutionLeavesUnverified(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-2")

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	first := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	first.ExecutionFingerprint = "execution-1"
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{finding}, nil, at))

	second := ledgerScan(t, store, "scan-2", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete)
	second.ExecutionFingerprint = "execution-1"
	testutil.FailErr(t, "fix under the old identity", store.RecordFindingEvents(t.Context(), &second, nil, []api.SecurityFinding{finding}, at.Add(time.Minute)))

	states := ledgerStates(t, store, root)
	if states["rule-a"] != api.FindingLedgerUnverified {
		t.Fatalf("state after the engine identity moved = %q, want unverified", states["rule-a"])
	}

	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if len(page.Entries) != 1 || page.Entries[0].Absence == nil || !page.Entries[0].Absence.ExecutionMoved {
		t.Fatalf("absence facts = %+v, want execution_moved", page.Entries[0].Absence)
	}
}

func TestLedgerFiltersPagesAndCounts(t *testing.T) {
	root := ledgerRoot(t)
	execution := "execution-1"
	store := ledgerFixture(t, root, "sast", execution)

	high := scanfindings.FixtureFinding("rule-high", api.FindingLevelHigh, "credential in a header", "api/authz.go", 11)
	low := scanfindings.FixtureFinding("rule-low", api.FindingLevelLow, "deferred close in a loop", "scan/runner.go", 22)
	gone := scanfindings.FixtureFinding("rule-gone", api.FindingLevelMedium, "unbounded read", "scan/parser.go", 33)
	first := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, high, low, gone)
	first.ExecutionFingerprint = execution
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{high, low, gone}, nil, at))

	second := ledgerScan(t, store, "scan-2", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, high, low)
	second.ExecutionFingerprint = execution
	testutil.FailErr(t, "fix", store.RecordFindingEvents(t.Context(), &second, nil, []api.SecurityFinding{gone}, at.Add(time.Minute)))

	all, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if all.TotalMatch != 3 {
		t.Fatalf("total = %d, want 3", all.TotalMatch)
	}
	if all.Counts.Open != 2 || all.Counts.Fixed != 1 {
		t.Fatalf("counts = %+v, want 2 open and 1 fixed", all.Counts)
	}
	if all.ByLevel["high"] != 1 || all.ByLevel["unknown"] != 0 {
		t.Fatalf("by_level = %+v, want every level present with high at 1", all.ByLevel)
	}

	open, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{
		States: []api.FindingLedgerState{api.FindingLedgerOpen}, Limit: 100,
	})
	testutil.FailErr(t, "read open", err)
	if open.TotalMatch != 2 {
		t.Fatalf("open total = %d, want 2", open.TotalMatch)
	}
	// Counts ignore the query's filters.
	if open.Counts.Fixed != 1 {
		t.Fatalf("counts under a filter = %+v, want the project's totals", open.Counts)
	}

	text, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Text: "CREDENTIAL", Limit: 100})
	testutil.FailErr(t, "read text filter", err)
	if text.TotalMatch != 1 || text.Entries[0].Finding.RuleID != "rule-high" {
		t.Fatalf("text filter matched %d rows", text.TotalMatch)
	}

	path, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Path: "scan", Limit: 100})
	testutil.FailErr(t, "read path filter", err)
	if path.TotalMatch != 2 {
		t.Fatalf("path prefix matched %d rows, want 2", path.TotalMatch)
	}

	firstPage, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 2})
	testutil.FailErr(t, "read first page", err)
	if len(firstPage.Entries) != 2 || firstPage.NextCursor == "" {
		t.Fatalf("first page = %d entries, next %q", len(firstPage.Entries), firstPage.NextCursor)
	}
	// Default order is most severe first.
	if firstPage.Entries[0].Finding.Level != api.FindingLevelHigh {
		t.Fatalf("first row = %q, want the most severe", firstPage.Entries[0].Finding.Level)
	}
	lastPage, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 2, Cursor: firstPage.NextCursor})
	testutil.FailErr(t, "read last page", err)
	if len(lastPage.Entries) != 1 || lastPage.NextCursor != "" {
		t.Fatalf("last page = %d entries, next %q", len(lastPage.Entries), lastPage.NextCursor)
	}
}

// A deselected scanner's rows are hidden, not deleted.
func TestLedgerScopesToSelectedScanners(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	scan := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	scan.ExecutionFingerprint = "execution-1"
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &scan, []api.SecurityFinding{finding}, nil, at))

	testutil.FailErr(t, "deselect scanner", store.DeleteSeries(t.Context(), root, "sast"))
	page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if page.TotalMatch != 0 {
		t.Fatalf("a deselected scanner still reported %d rows", page.TotalMatch)
	}

	testutil.FailErr(t, "reselect scanner", store.UpsertSeries(t.Context(), scanbase.SeriesRow{
		CanonicalPath: root, ScannerID: "sast",
		Categories:                      []api.ScanCategory{api.ScanCategorySAST},
		LastCoveredExecutionFingerprint: "execution-1",
		UpdatedAt:                       time.Now().UTC(),
	}))
	page, err = store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger after reselect", err)
	if page.TotalMatch != 1 {
		t.Fatalf("reselecting the scanner restored %d rows, want 1", page.TotalMatch)
	}
}

func TestLedgerIgnoreDisplacesOpenAndIsWithdrawable(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	cadence := ledgerCadence(store)

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	scan := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	scan.ExecutionFingerprint = "execution-1"
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(t.Context(), &scan, []api.SecurityFinding{finding}, nil, at))

	catalog, err := cadence.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{
		Path: "a.go", Reason: "fixture material",
	})
	testutil.FailErr(t, "add ignore", err)
	entryID := ""
	for _, rule := range catalog.Rules {
		if rule.Source == "project" {
			entryID = rule.ID
		}
	}
	if entryID == "" {
		t.Fatalf("the project entry was not written back: %+v", catalog.Rules)
	}

	page, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if len(page.Entries) != 1 || page.Entries[0].State != api.FindingLedgerIgnored {
		t.Fatalf("state under a live ignore = %+v, want ignored", page.Entries)
	}
	if page.Counts.Open != 0 || page.Counts.Ignored != 1 {
		t.Fatalf("counts = %+v, want the row counted once, as ignored", page.Counts)
	}
	if ignore := page.Entries[0].Ignore; ignore == nil || ignore.Reason != "fixture material" {
		t.Fatalf("ignore on the row = %+v, want the decision that covered it", page.Entries[0].Ignore)
	}

	// Withdrawal applies on the next read.
	if _, err := cadence.RemoveIgnore(t.Context(), root, entryID); err != nil {
		testutil.FailErr(t, "remove ignore", err)
	}
	page, err = cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if page.Entries[0].State != api.FindingLedgerOpen || page.Entries[0].Ignore != nil {
		t.Fatalf("state after withdrawal = %+v, want open with no decision", page.Entries[0])
	}
}

// A lapsed entry stops applying at read time and stays in the file.
func TestLedgerIgnoreExpiryReturnsTheFindingToOpen(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	cadence := ledgerCadence(store)

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	scan := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	scan.ExecutionFingerprint = "execution-1"
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(
		t.Context(), &scan, []api.SecurityFinding{finding}, nil, time.Now().UTC()))

	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if _, err := cadence.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{
		Path: "a.go", Reason: "was meant to be temporary", ExpiresOn: yesterday,
	}); err != nil {
		testutil.FailErr(t, "add ignore", err)
	}

	page, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	entry := page.Entries[0]
	if entry.State != api.FindingLedgerOpen {
		t.Fatalf("state under a lapsed ignore = %q, want open", entry.State)
	}
	// The lapsed entry is still reported on the row.
	if entry.Ignore == nil || !entry.Ignore.Expired {
		t.Fatalf("ignore on the row = %+v, want the lapsed decision", entry.Ignore)
	}
}

// A hand edit to the ignore file applies on the next read.
func TestLedgerAppliesAnIgnoreFileEditedOutsideTheHost(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	cadence := ledgerCadence(store)

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	scan := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	scan.ExecutionFingerprint = "execution-1"
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(
		t.Context(), &scan, []api.SecurityFinding{finding}, nil, time.Now().UTC()))
	if _, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 1}); err != nil {
		testutil.FailErr(t, "prime ledger", err)
	}

	path := projectignore.Path(root)
	testutil.FailErr(t, "mark ignore overlay format", settingsoverlay.EnsureCurrentFormat(root))
	testutil.FailErr(t, "overlay dir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write ignore file", os.WriteFile(path, []byte(
		"version: 1\nfindings:\n  - kind: sast\n    reason: handed to us by a teammate\n"), 0o644))

	page, err := cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if page.Entries[0].State != api.FindingLedgerIgnored {
		t.Fatalf("state after a hand edit = %q, want ignored", page.Entries[0].State)
	}
}

// Writes and reads both refuse an untrusted project's ignores.
func TestLedgerRefusesAnIgnoreForAnUntrustedProject(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	closed := &Service{Store: store}

	finding := scanfindings.FixtureFinding("rule-a", api.FindingLevelHigh, "a", "a.go", 1)
	scan := ledgerScan(t, store, "scan-1", root, "sast", api.ScanTargetPaths, api.ScanCoverageComplete, finding)
	scan.ExecutionFingerprint = "execution-1"
	testutil.FailErr(t, "introduce", store.RecordFindingEvents(
		t.Context(), &scan, []api.SecurityFinding{finding}, nil, time.Now().UTC()))

	if _, err := closed.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{
		Path: "a.go", Reason: "not going to happen",
	}); !errors.Is(err, scanignore.ErrIgnoreProjectNotTrusted) {
		t.Fatalf("err = %v, want ErrIgnoreProjectNotTrusted", err)
	}

	// An existing file is not applied either.
	path := projectignore.Path(root)
	testutil.FailErr(t, "mark ignore overlay format", settingsoverlay.EnsureCurrentFormat(root))
	testutil.FailErr(t, "overlay dir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write ignore file", os.WriteFile(path,
		[]byte("version: 1\nfindings:\n  - path: a.go\n    reason: snuck in\n"), 0o644))
	page, err := closed.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 10})
	testutil.FailErr(t, "read ledger", err)
	if page.Entries[0].State != api.FindingLedgerOpen {
		t.Fatalf("state = %q, want an untrusted project's ignore file to be inert", page.Entries[0].State)
	}
}

func TestLedgerRejectsAnIgnoreEntryNamingNothing(t *testing.T) {
	root := ledgerRoot(t)
	cadence := ledgerCadence(ledgerFixture(t, root, "sast", "execution-1"))
	if _, err := cadence.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{Reason: "everything"}); !errors.Is(err, scanignore.ErrIgnoreNoPredicate) {
		t.Fatalf("an entry with no predicate was accepted: %v", err)
	}
	if _, err := cadence.AddIgnore(t.Context(), root, api.FindingIgnoreEntry{Path: "a.go"}); !errors.Is(err, scanignore.ErrIgnoreNoReason) {
		t.Fatalf("an entry with no reason was accepted: %v", err)
	}
}

func TestLedgerSortOrdersPersistedFindings(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	firstFinding := scanfindings.FixtureFinding("first", api.FindingLevelLow, "Zulu", "z.go", 20)
	secondFinding := scanfindings.FixtureFinding("second", api.FindingLevelHigh, "alpha", "A.go", 10)
	first := ledgerScan(t, store, "sort-first", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, firstFinding)
	at := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "introduce first finding", store.RecordFindingEvents(t.Context(), &first, []api.SecurityFinding{firstFinding}, nil, at))
	second := ledgerScan(t, store, "sort-second", root, "sast", api.ScanTargetFull, api.ScanCoverageComplete, secondFinding)
	testutil.FailErr(t, "introduce second finding", store.RecordFindingEvents(t.Context(), &second, []api.SecurityFinding{secondFinding}, nil, at.Add(time.Minute)))
	testutil.FailErr(t, "fix first finding", store.RecordFindingEvents(t.Context(), &second, nil, []api.SecurityFinding{firstFinding}, at.Add(2*time.Minute)))
	for _, tc := range []struct {
		sort  api.FindingLedgerSort
		order string
		first string
	}{
		{api.FindingLedgerSortSeverity, "", "second"},
		{api.FindingLedgerSortSeverity, "asc", "first"},
		{api.FindingLedgerSortFinding, "", "second"},
		{api.FindingLedgerSortFinding, "desc", "first"},
		{api.FindingLedgerSortLocation, "", "second"},
		{api.FindingLedgerSortLocation, "desc", "first"},
		{api.FindingLedgerSortState, "", "second"},
		{api.FindingLedgerSortState, "asc", "first"},
		{api.FindingLedgerSortFirstSeen, "", "second"},
		{api.FindingLedgerSortFirstSeen, "asc", "first"},
		{api.FindingLedgerSortLastSeen, "", "first"},
		{api.FindingLedgerSortLastSeen, "asc", "second"},
	} {
		t.Run(string(tc.sort)+"/"+tc.order, func(t *testing.T) {
			page, err := store.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Sort: tc.sort, Order: tc.order})
			testutil.FailErr(t, "read sorted ledger", err)
			if len(page.Entries) != 2 || page.Entries[0].Finding.RuleID != tc.first {
				t.Fatalf("sorted ledger = %+v, want %s first", page.Entries, tc.first)
			}
		})
	}
}
