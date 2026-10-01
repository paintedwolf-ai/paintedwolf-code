package execution

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSpillResultJSON_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	findings := make([]api.SecurityFinding, 0, 200)
	for i := 0; i < 200; i++ {
		findings = append(findings, api.SecurityFinding{
			RuleID:  "rule-" + strings.Repeat("x", 200),
			Message: strings.Repeat("secret-finding-", 40),
			Locations: []api.SecurityFindingLocation{{
				URI: "path/to/file-" + strings.Repeat("a", 80) + ".go",
			}},
		})
	}
	result := &scanoutput.Result{
		FindingsCount: len(findings),
		Findings:      findings,
		Warnings:      []api.ScanWarning{{Kind: api.ScanWarningRuleParseError, Message: "warn"}},
		Categories:    []api.ScanCategory{api.ScanCategorySecret},
	}
	full, err := json.Marshal(result)
	testutil.FailErr(t, "marshal full", err)
	capBytes := len(full) / 4
	if capBytes < 1024 {
		capBytes = 1024
	}

	raw, err := scanbase.SpillResultJSON(result, capBytes, filepath.Join(dir, scanbase.ScanResultSpillDir), "scan-1")
	testutil.FailErr(t, "spill", err)
	if len(raw) >= len(full) {
		t.Fatalf("stub size %d not smaller than full %d", len(raw), len(full))
	}
	var stub scanoutput.Result
	testutil.FailErr(t, "unmarshal stub", json.Unmarshal(raw, &stub))
	if stub.ResultSpillPath == "" {
		t.Fatal("want result_spill_path")
	}
	if stub.FindingsCount != len(findings) {
		t.Fatalf("FindingsCount = %d", stub.FindingsCount)
	}
	if len(stub.Findings) != 0 {
		t.Fatal("stub must not embed findings")
	}
	if len(stub.Warnings) != 1 {
		t.Fatalf("warnings = %d", len(stub.Warnings))
	}

	loaded, err := scanbase.LoadSpilledResult(dir, stub.ResultSpillPath)
	testutil.FailErr(t, "load spill", err)
	if loaded.FindingsCount != len(findings) || len(loaded.Findings) != len(findings) {
		t.Fatalf("loaded = counts %d/%d", loaded.FindingsCount, len(loaded.Findings))
	}
}

func TestMarkCompleteSpillKeepsDatabaseResultSmall(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "spill.db")
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	store := scanbase.NewSQLStore(sqlDB)

	canonical := t.TempDir()
	job := api.CodeScan{
		ID:            "spill-scan",
		CanonicalPath: canonical,
		Status:        api.CodeScanStatusPending,
		ScannerID:     "lycaon-secrets",
		Categories:    []api.ScanCategory{api.ScanCategorySecret},
	}
	testutil.FailErr(t, "insert", store.Insert(context.Background(), job, nil, ""))
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	if claimed.ID != job.ID {
		t.Fatalf("claimed scan %q want %q", claimed.ID, job.ID)
	}

	payload := strings.Repeat("leak-", 1<<11)
	findings := make([]api.SecurityFinding, 40)
	for i := range findings {
		findings[i] = api.SecurityFinding{
			RuleID:    "r",
			Message:   payload,
			Locations: []api.SecurityFindingLocation{{URI: "f.go"}},
		}
	}
	result := &scanoutput.Result{FindingsCount: len(findings), Findings: findings}

	dataDir := t.TempDir()
	cfg := scancfg.DefaultRunnerConfig()
	cfg.Runner.ResultSpillBytes = 64 << 10
	runner := NewRunner(store, nil, nil, cfg, nil)
	runner.DataDir = dataDir

	won, err := runner.markCompleteWithAuthority(context.Background(), claimed, result, findings, nil, api.ScanCoverageComplete)
	testutil.FailErr(t, "markCompleteWithAuthority", err)
	if !won {
		t.Fatal("markComplete lost the terminal CAS")
	}

	got, err := store.Get(context.Background(), job.ID)
	testutil.FailErr(t, "get", err)
	if len(got.Result) > 64<<10 {
		t.Fatalf("result_json len = %d want <= 64KiB stub", len(got.Result))
	}
	var stub scanoutput.Result
	testutil.FailErr(t, "decode", json.Unmarshal(got.Result, &stub))
	if stub.ResultSpillPath == "" {
		t.Fatal("expected spill path on stub")
	}
	evidenceRoot := project.PathKeyedHostDataDir(dataDir, canonical)
	loaded, err := scanbase.LoadSpilledResult(evidenceRoot, stub.ResultSpillPath)
	testutil.FailErr(t, "hydrate", err)
	if len(loaded.Findings) != len(findings) {
		t.Fatalf("hydrated findings = %d", len(loaded.Findings))
	}
}
