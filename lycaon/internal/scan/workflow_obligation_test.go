package scan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWorkflowScanLedger struct {
	scans  []api.CodeScan
	passes []FullPass
	err    error
}

func (s stubWorkflowScanLedger) ListByWorkflowRunID(context.Context, string) ([]api.CodeScan, error) {
	return append([]api.CodeScan(nil), s.scans...), s.err
}

func (s stubWorkflowScanLedger) FullPassesForWorkflowRun(context.Context, string) ([]FullPass, error) {
	return append([]FullPass(nil), s.passes...), s.err
}

func TestWorkflowObligationValidateParams(t *testing.T) {
	obligation := NewWorkflowObligationSpec()
	for _, tc := range []struct {
		name   string
		params map[string]any
		valid  bool
	}{
		{name: "categories", params: map[string]any{"categories": []any{"security"}}, valid: true},
		{name: "missing categories", params: map[string]any{}},
		{name: "empty categories", params: map[string]any{"categories": []any{}}},
		{name: "unknown parameter", params: map[string]any{"categories": []any{"security"}, "mode": "fast"}},
		{name: "unknown category", params: map[string]any{"categories": []any{"unknown"}}},
		{name: "non-string category", params: map[string]any{"categories": []any{1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := obligation.ValidateParams(tc.params)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateParams error = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestWorkflowObligationStatusCountsThePassItAskedFor(t *testing.T) {
	complete := api.CodeScan{ScannerID: "lycaon-sca", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete}
	for _, tc := range []struct {
		name      string
		scans     []api.CodeScan
		phase     api.FullPassMemberPhase
		want      string
		wantError string
	}{
		{name: "waiting with no scans yet", phase: api.FullPassMemberWaitingForScanner, want: api.ObligationStatusPending},
		{name: "waiting beside a finished member", scans: []api.CodeScan{complete}, phase: api.FullPassMemberWaitingForPass, want: api.ObligationStatusPending},
		{
			name: "member that never started", scans: []api.CodeScan{complete}, phase: api.FullPassMemberNotStarted,
			want: api.ObligationStatusFailed, wantError: "scanner(s) lycaon-sast did not start",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pass := FullPass{ID: "pass-1", Members: []FullPassMember{
				{ScannerID: "lycaon-sca", Phase: api.FullPassMemberStarted, Scan: &complete},
				{ScannerID: "lycaon-sast", Phase: tc.phase},
			}}
			obligation := &WorkflowObligation{
				Ledger: stubWorkflowScanLedger{scans: tc.scans, passes: []FullPass{pass}},
				Params: func(context.Context, string, string, string) (map[string]any, error) {
					return map[string]any{"categories": []any{"security"}, "full": true}, nil
				},
			}
			got, err := obligation.Status(context.Background(), "run-1", "ingest")
			testutil.FailErr(t, "status", err)
			if got.Status != tc.want || got.Error != tc.wantError {
				t.Fatalf("status = %#v", got)
			}
			if tc.want == api.ObligationStatusPending {
				if pending, _ := got.Detail["engines_pending"].([]string); len(pending) != 1 || pending[0] != "lycaon-sast" {
					t.Fatalf("engines_pending = %#v", got.Detail["engines_pending"])
				}
			}
		})
	}
}

func TestWorkflowObligationStatus(t *testing.T) {
	tests := []struct {
		name       string
		scans      []api.CodeScan
		wantStatus string
		wantError  string
		findings   int
		terminal   int
	}{
		{name: "empty", wantStatus: api.ObligationStatusEmpty},
		{
			name: "pending",
			scans: []api.CodeScan{
				{ScannerID: "sast", Status: api.CodeScanStatusRunning, FindingsCount: 2},
				{ScannerID: "secrets", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete, FindingsCount: 1},
			},
			wantStatus: api.ObligationStatusPending,
			findings:   3,
			terminal:   1,
		},
		{
			name: "failed",
			scans: []api.CodeScan{
				{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete, FindingsCount: 2},
				{Status: api.CodeScanStatusFailed, Error: "scanner unavailable"},
			},
			wantStatus: api.ObligationStatusFailed,
			wantError:  "scanner unavailable",
			findings:   2,
			terminal:   2,
		},
		{
			name:       "complete",
			scans:      []api.CodeScan{{Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoverageComplete, FindingsCount: 4}},
			wantStatus: api.ObligationStatusComplete,
			findings:   4,
			terminal:   1,
		},
		{
			name:       "missing coverage",
			scans:      []api.CodeScan{{ScannerID: "sast", Status: api.CodeScanStatusComplete}},
			wantStatus: api.ObligationStatusFailed,
			wantError:  "scanner sast coverage is ",
			terminal:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obligation := &WorkflowObligation{
				Ledger: stubWorkflowScanLedger{scans: tt.scans},
				Params: func(context.Context, string, string, string) (map[string]any, error) {
					return map[string]any{"categories": []any{"security"}, "full": true}, nil
				},
			}
			got, err := obligation.Status(context.Background(), "run-1", "ingest")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if got.Status != tt.wantStatus || got.Error != tt.wantError {
				t.Fatalf("status = %#v", got)
			}
			if len(tt.scans) == 0 {
				return
			}
			if got.Detail["findings_count"] != tt.findings || got.Detail["scans_terminal"] != tt.terminal {
				t.Fatalf("detail = %#v", got.Detail)
			}
			if ids, ok := got.Detail["scan_ids"].([]string); !ok || len(ids) != len(tt.scans) {
				t.Fatalf("run-bound scan_ids = %#v", got.Detail["scan_ids"])
			}
			if rows, ok := got.Detail["per_scan"].([]map[string]any); !ok || len(rows) != len(tt.scans) {
				t.Fatalf("run-bound per_scan = %#v", got.Detail["per_scan"])
			}
		})
	}
}

func TestWorkflowEvidenceDigest(t *testing.T) {
	digest := WorkflowEvidenceDigest(stubWorkflowScanLedger{scans: []api.CodeScan{{
		ID: "scan-1", Status: api.CodeScanStatusFailed, FindingsCount: 2, Error: "scanner unavailable",
	}}})(context.Background(), "run-1")
	want := "## Scan ledger\n- `scan-1` failed · 2 finding(s) · scanner unavailable"
	if digest != want {
		t.Fatalf("digest = %q, want %q", digest, want)
	}
}

func TestWorkflowWorkerScanDigestCarriesExactBoundSet(t *testing.T) {
	digest, err := WorkflowWorkerScanDigest(context.Background(), stubWorkflowScanLedger{scans: []api.CodeScan{
		{ID: "scan-run-sast", ScannerID: "sast", Status: api.CodeScanStatusComplete, FindingsCount: 2},
		{ID: "scan-run-secret", ScannerID: "secret", Status: api.CodeScanStatusFailed, Error: "unavailable"},
	}}, "run-1")
	testutil.FailErr(t, "workflow worker scan digest", err)
	joined := strings.Join(digest, "\n")
	for _, want := range []string{"scan-run-sast", "scan-run-secret", `scan_ids: ["scan-run-sast","scan-run-secret"]`, "Do not substitute project-ambient scans"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("workflow scan digest missing %q:\n%s", want, joined)
		}
	}
}

func TestWorkflowWorkerScanDigestReportsMissingBoundSet(t *testing.T) {
	digest, err := WorkflowWorkerScanDigest(t.Context(), stubWorkflowScanLedger{}, "run-1")
	testutil.FailErr(t, "workflow worker scan digest", err)
	if joined := strings.Join(digest, "\n"); !strings.Contains(joined, "No scans are bound") {
		t.Fatalf("missing bound-set disclosure: %q", joined)
	}
}

func TestWorkflowWorkerScanDigestPropagatesLedgerFailure(t *testing.T) {
	want := errors.New("ledger unavailable")
	_, err := WorkflowWorkerScanDigest(t.Context(), stubWorkflowScanLedger{err: want}, "run-1")
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestWorkflowObligationReportsLookupErrors(t *testing.T) {
	for _, step := range []string{"params", "run", "project"} {
		t.Run(step, func(t *testing.T) {
			lookupErr := errors.New("lookup unavailable")
			obligation := &WorkflowObligation{
				Ledger: stubWorkflowScanLedger{}, History: authorityTestStore(t),
				Params: func(context.Context, string, string, string) (map[string]any, error) {
					if step == "params" {
						return nil, lookupErr
					}
					return map[string]any{"categories": []any{"security"}}, nil
				},
				Runs: func(context.Context, string) (*api.WorkflowRun, error) {
					if step == "run" {
						return nil, lookupErr
					}
					return &api.WorkflowRun{ProjectID: "project"}, nil
				},
				Projects: func(context.Context, string) (string, error) { return "", lookupErr },
			}
			_, err := obligation.Status(t.Context(), "run", "ingest")
			if !errors.Is(err, lookupErr) {
				t.Fatalf("lookup error = %v, want %v", err, lookupErr)
			}
		})
	}
}

// The phase note follows the newest pass the run is bound to: the one its
// latest phase entry requested or joined.
func TestWorkflowObligationExplainFullPassNamesNewestPass(t *testing.T) {
	obligation := &WorkflowObligation{Ledger: stubWorkflowScanLedger{passes: []FullPass{{ID: "pass-new"}, {ID: "pass-old"}}}}
	run := &api.WorkflowRun{ID: "run-1", ProjectID: "project-1"}
	full := map[string]any{"categories": []any{"security"}, "full": true}

	progress, err := obligation.ExplainFullPass(context.Background(), run, full)
	testutil.FailErr(t, "explain progress", err)
	if progress == nil || progress.AssessmentID != "pass-new" || progress.ProjectID != "project-1" {
		t.Fatalf("pass = %+v", progress)
	}

	for name, tc := range map[string]struct {
		run    *api.WorkflowRun
		params map[string]any
		ledger stubWorkflowScanLedger
	}{
		"delta obligation":    {run: run, params: map[string]any{"categories": []any{"security"}}, ledger: obligation.Ledger.(stubWorkflowScanLedger)},
		"no bound pass":       {run: run, params: full},
		"run with no project": {run: &api.WorkflowRun{ID: "run-1"}, params: full, ledger: obligation.Ledger.(stubWorkflowScanLedger)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := (&WorkflowObligation{Ledger: tc.ledger}).ExplainFullPass(context.Background(), tc.run, tc.params)
			testutil.FailErr(t, "explain full pass", err)
			if got != nil {
				t.Fatalf("pass = %+v, want none", got)
			}
		})
	}
}

func TestWorkflowWorkerDigestSeparatesExecutionFromCoverage(t *testing.T) {
	digest, err := WorkflowWorkerScanDigest(t.Context(), stubWorkflowScanLedger{scans: []api.CodeScan{{ID: "sast", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial, FindingsCount: 226}}}, "run")
	testutil.FailErr(t, "read run scan digest", err)
	joined := strings.Join(digest, "\n")
	for _, fact := range []string{"execution_status=complete", "coverage_status=partial", "result_available=true", "findings_count=226"} {
		if !strings.Contains(joined, fact) {
			t.Fatalf("missing independent scan fact %s: %s", fact, joined)
		}
	}
}
