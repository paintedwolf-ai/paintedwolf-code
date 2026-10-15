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

func ledgerCadence(store *scanbase.SQLStore) *Service {
	return &Service{
		IgnoreService: IgnoreService{
			Store: store,
			OverlayRootsApply: func(_ context.Context, rootPaths []string) []string {
				return rootPaths
			},
		},
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

	if _, err := cadence.RemoveIgnore(t.Context(), root, entryID); err != nil {
		testutil.FailErr(t, "remove ignore", err)
	}
	page, err = cadence.FindingLedger(t.Context(), root, api.FindingLedgerQueryRequest{Limit: 100})
	testutil.FailErr(t, "read ledger", err)
	if page.Entries[0].State != api.FindingLedgerOpen || page.Entries[0].Ignore != nil {
		t.Fatalf("state after withdrawal = %+v, want open with no decision", page.Entries[0])
	}
}

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
	if entry.Ignore == nil || !entry.Ignore.Expired {
		t.Fatalf("ignore on the row = %+v, want the lapsed decision", entry.Ignore)
	}
}

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

func TestLedgerRefusesAnIgnoreForAnUntrustedProject(t *testing.T) {
	root := ledgerRoot(t)
	store := ledgerFixture(t, root, "sast", "execution-1")
	closed := &Service{IgnoreService: IgnoreService{Store: store}}

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
