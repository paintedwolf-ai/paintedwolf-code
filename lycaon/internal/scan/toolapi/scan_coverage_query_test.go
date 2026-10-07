package toolapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	scancoverage "github.com/lycaon/lycaon/internal/scan/coverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeCoverageQuerySummarizesFullScopeAndPagesLocations(t *testing.T) {
	reg, _, store, dir := registerNativeScanTools(t)
	for _, id := range []string{"scan-a", "scan-b"} {
		testutil.FailErr(t, "insert scan", store.Insert(t.Context(), api.CodeScan{ID: id, CanonicalPath: dir, ScannerID: "scanner", Status: api.CodeScanStatusComplete, CreatedAt: time.Now().UTC()}, nil, ""))
		var warnings []api.ScanWarning
		for i := range 25 {
			warnings = append(warnings, api.ScanWarning{Kind: "file_partial_semantics", File: fmt.Sprintf("service/src/f%02d.go", i), StartLine: i + 1, Construct: "indirect", Message: strings.Repeat("large diagnostic ", 100)})
		}
		warnings = append(warnings, api.ScanWarning{Kind: "file_partial_semantics", File: "service-extra/outside.go", Construct: "other"})
		testutil.FailErr(t, "save warnings", store.SaveIngest(t.Context(), id, evidence.Record{Artifacts: map[string]any{"warnings": warnings}}))
	}
	args := map[string]any{"scan_ids": []string{"scan-b", "scan-a"}, "view": "coverage", "path": "service", "warning_kind": "file_partial_semantics", "construct": "indirect", "limit": 7}
	seen := map[string]bool{}
	for offset := 0; ; {
		args["offset"] = offset
		raw, err := reg.Run(t.Context(), "scan_query", args, scanToolContext("session", dir))
		testutil.FailErr(t, "query coverage", err)
		var out struct {
			Scope     scancoverage.Scope `json:"scope"`
			Locations []struct {
				ScanID           string `json:"scan_id"`
				Path             string `json:"path"`
				Line             int    `json:"line"`
				Message          string `json:"message"`
				MessageTruncated bool   `json:"message_truncated"`
			} `json:"locations"`
			Total int  `json:"total_match"`
			Next  *int `json:"next_offset"`
		}
		testutil.FailErr(t, "decode coverage", json.Unmarshal([]byte(raw), &out))
		if out.Scope.Files != 25 || out.Scope.Warnings != 50 || out.Total != 50 || len(out.Locations) > 7 {
			t.Fatalf("scope/page = %+v", out)
		}
		if len(raw) > 15000 {
			t.Fatal("coverage page exceeded representative payload bound")
		}
		for _, row := range out.Locations {
			if len([]rune(row.Message)) > 320 || !row.MessageTruncated {
				t.Fatal("diagnostic message not bounded explicitly")
			}
			key := row.ScanID + ":" + row.Path
			if row.ScanID == "" || seen[key] {
				t.Fatalf("missing scan identity or repeated location: %+v", row)
			}
			seen[key] = true
		}
		if out.Next == nil {
			break
		}
		if *out.Next <= offset {
			t.Fatal("pagination did not progress")
		}
		offset = *out.Next
	}
	if len(seen) != 50 {
		t.Fatalf("paged %d warnings", len(seen))
	}
	args["view"] = "findings"
	if _, err := reg.Run(t.Context(), "scan_query", args, scanToolContext("session", dir)); err == nil {
		t.Fatal("finding query silently ignored coverage filters")
	}
	args["view"] = "coverage"
	args["kind"] = "sast"
	if _, err := reg.Run(t.Context(), "scan_query", args, scanToolContext("session", dir)); err == nil {
		t.Fatal("coverage query silently ignored finding filters")
	}
}
