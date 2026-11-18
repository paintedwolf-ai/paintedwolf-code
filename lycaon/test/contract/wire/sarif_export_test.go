package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

func TestSARIFExportRoundTripOpenGrepIngest(t *testing.T) {
	t.Parallel()
	reg := scanoutput.DefaultParserRegistry()
	parser, err := reg.Get(scanoutput.OutputParserOpengrepJSON)
	contractcheck.FailErr(t, "parser registry", err)

	raw := []byte(`{
  "results": [{
    "check_id": "lycaon.ruby.sql-string-concat",
    "path": "app/models/user.rb",
    "start": {"line": 12, "col": 4},
    "end": {"line": 12, "col": 24},
    "extra": {"message": "SQL built via string concat", "severity": "ERROR"}
  }],
  "errors": []
}`)
	parsed, err := parser.Parse(raw)
	contractcheck.FailErr(t, "parse opengrep fixture", err)
	if len(parsed.Findings) != 1 {
		t.Fatalf("parsed %d findings, want 1", len(parsed.Findings))
	}

	sqlDB := testdbfixture.Open(t, "store.db")
	store := scan.NewSQLStore(sqlDB)
	coord := scantest.Coordinator(t, store, nil)
	created, err := coord.Enqueue(context.Background(), scan.EnqueueRequest{
		ProjectDir: t.TempDir(),
		Categories: []api.ScanCategory{api.ScanCategorySAST},
		ScannerID:  "opengrep-sast",
	})
	contractcheck.FailErr(t, "enqueue", err)
	claimed, err := store.ClaimNext(t.Context())
	contractcheck.FailErr(t, "claim scan", err)
	if claimed.ID != created.ID {
		t.Fatalf("claimed scan %q want %q", claimed.ID, created.ID)
	}
	if _, err := store.MarkComplete(context.Background(), claimed, parsed); err != nil {
		testutil.FailErr(t, "MarkComplete", err)
	}
	rec := evidence.Record{
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"findings":        parsed.Findings,
			"findings_count":  len(parsed.Findings),
			"findings_stored": len(parsed.Findings),
		},
	}
	if err := store.SaveIngest(context.Background(), created.ID, rec); err != nil {
		testutil.FailErr(t, "SaveIngest", err)
	}

	got, err := store.Get(context.Background(), created.ID)
	contractcheck.FailErr(t, "Get scan", err)
	data, err := scanoutput.ExportScanSARIF(got)
	contractcheck.FailErr(t, "ExportScanSARIF", err)
	report, err := sarif.FromBytes(data)
	contractcheck.FailErr(t, "sarif.FromBytes", err)
	if len(report.Runs) != 1 || len(report.Runs[0].Results) != 1 {
		t.Fatalf("exported %d runs, want one run with one result: %#v", len(report.Runs), report.Runs)
	}
	result := report.Runs[0].Results[0]
	if result.RuleID == nil || *result.RuleID != "opengrep:lycaon.ruby.sql-string-concat" {
		t.Fatalf("rule id = %q, want the opengrep check id", deref(result.RuleID))
	}
	if result.Level == nil || *result.Level != "error" {
		t.Fatalf("level = %q, want error for an ERROR finding", deref(result.Level))
	}
	if len(result.Locations) != 1 {
		t.Fatalf("locations = %d, want 1", len(result.Locations))
	}
	physical := result.Locations[0].PhysicalLocation
	if physical == nil || physical.ArtifactLocation == nil || physical.ArtifactLocation.URI == nil || *physical.ArtifactLocation.URI != "app/models/user.rb" {
		t.Fatalf("location = %#v, want app/models/user.rb", physical)
	}
	if physical.Region == nil || physical.Region.StartLine == nil || *physical.Region.StartLine != 12 {
		t.Fatalf("region = %#v, want start line 12", physical.Region)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
