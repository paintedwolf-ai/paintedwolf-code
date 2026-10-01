package scan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindingEntryQueryMatchesFindingFilters(t *testing.T) {
	findings := []api.SecurityFinding{
		scanfindings.FixtureFinding("rule-high", api.FindingLevelHigh, "one", "src/main.go", 1),
		scanfindings.FixtureFinding("rule-low", api.FindingLevelLow, "two", "tests/main.go", 2),
		scanfindings.FixtureFinding("rule-high", api.FindingLevelHigh, "one", "src/main.go", 1),
		scanfindings.FixtureFinding("rule-unlocated", api.FindingLevelLow, "", "", 0),
		scanfindings.FixtureFinding("rule-unlocated", api.FindingLevelLow, "", "", 0),
	}
	findings[0] = scanfindings.SetHintCode(findings[0], "SCAN_TEST")
	findings[0].Properties.Lycaon.Advisory = &api.AdvisoryRef{
		OSVID: "CVE-2026-0001", CVEIDs: []string{"CVE-2026-0001"}, GHSAIDs: []string{"GHSA-6vm3-jj99-7229"}, Aliases: []string{"GO-2026-0001"},
	}
	store, scan := seedFindingEntryQuery(t, findings)
	cur1, err := scanQueryPages.Encode(queryScope(QueryRequest{ScanID: scan.ID}), 1)
	testutil.FailErr(t, "encode cursor 1", err)
	cur100, err := scanQueryPages.Encode(queryScope(QueryRequest{ScanID: scan.ID}), 100)
	testutil.FailErr(t, "encode cursor 100", err)
	curDedupe, err := scanQueryPages.Encode(queryScope(QueryRequest{ScanID: scan.ID, Dedupe: true}), 1)
	testutil.FailErr(t, "encode cursor dedupe", err)
	for _, req := range []QueryRequest{
		{}, {Dedupe: true}, {Level: "HIGH"}, {Kind: "custom"}, {Fingerprint: "ONE"},
		{Code: "scan_test"}, {RuleID: "high"}, {Path: "src/"}, {Path: "missing/"},
		{AdvisoryID: " cve-2026-0001 "}, {AdvisoryID: "ghsa-6VM3-jj99-7229"}, {AdvisoryID: "go-2026-0001"}, {AdvisoryID: "cve-2026-9999"},
		{Cursor: cur1, Limit: 2}, {Cursor: cur100}, {Dedupe: true, Cursor: curDedupe, Limit: 1},
	} {
		t.Run(fmt.Sprintf("%+v", req), func(t *testing.T) {
			got, err := store.queryFindingEntries(t.Context(), scan, req)
			testutil.FailErr(t, "query entries", err)
			want, err := QuerySecurityFindings(scan.ID, findings, nil, req)
			testutil.FailErr(t, "query reference", err)
			if len(got.Findings) == 0 {
				got.Findings = nil
			}
			if len(want.Findings) == 0 {
				want.Findings = nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query mismatch: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestFindingEntryQueryBoundsPageWithoutLosingFindings(t *testing.T) {
	findings := make([]api.SecurityFinding, maxQueryPageSize+5)
	for i := range findings {
		findings[i] = scanfindings.FixtureFinding("rule", api.FindingLevelHigh, fmt.Sprint(i), "src/main.go", i+1)
	}
	store, scan := seedFindingEntryQuery(t, findings)
	first, err := store.queryFindingEntries(t.Context(), scan, QueryRequest{Limit: 100000})
	testutil.FailErr(t, "read bounded page", err)
	if len(first.Findings) != maxQueryPageSize || first.TotalMatch != len(findings) || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	last, err := store.queryFindingEntries(t.Context(), scan, QueryRequest{Cursor: first.NextCursor})
	testutil.FailErr(t, "read remaining page", err)
	if len(last.Findings) != 5 || last.Truncated || last.NextCursor != "" {
		t.Fatalf("last page = %+v", last)
	}
}

func seedFindingEntryQuery(t *testing.T, findings []api.SecurityFinding) (*SQLStore, *api.CodeScan) {
	t.Helper()
	store := authorityTestStore(t)
	scan := authorityScan("query", "assessment", t.TempDir(), "snapshot", "sast", api.ScanTargetFull, nil)
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), scan, nil, ""))
	completeNextScan(t, store)
	stored, err := (&CoordinatorImpl{Store: store}).Summary(t.Context(), scan.ID)
	testutil.FailErr(t, "read summary", err)
	for i, finding := range findings {
		raw, err := json.Marshal(finding)
		testutil.FailErr(t, "marshal finding", err)
		testutil.FailErr(t, "insert finding", store.queries.InsertFindingEntry(t.Context(), db.InsertFindingEntryParams{
			FindingSetID: stored.FindingSetID, Ordinal: int64(i), FindingJson: string(raw),
		}))
	}
	return store, stored
}
