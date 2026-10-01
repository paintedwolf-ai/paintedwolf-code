package survey_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func testScope(t *testing.T, dir string) survey.Scope {
	t.Helper()
	boundary := sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{"grep": true, "find": true, "list_dir": true},
	}})
	return survey.Scope{
		Boundary: boundary,
		ToolCtx: tools.ToolContext{
			Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1",
			Agent:        tools.DefaultToolProfileID,
		},
	}
}

func writeFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"main.go":              "package main\nfunc main() { _ = os.Getenv(\"HOME\") }\n",
		"internal/store.go":    "package internal\n// store\n",
		"internal/api.go":      "package internal\n// GET /v1/health\n",
		"pkg/routes/routes.go": "package routes\n// HandleFunc(\"/v1/foo\")\n",
	}
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	return dir
}

func TestRunnerFixtureRepo(t *testing.T) {
	dir := writeFixtureRepo(t)
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"ssot_drift": {
			ID: "ssot_drift",
			Probes: []survey.Probe{
				{Kind: survey.ProbeGrep, Pattern: "Getenv", Path: ".", Label: "env_reads", Priority: 10},
				{Kind: survey.ProbeGrep, Pattern: "/v1/", Path: ".", Label: "routes", Priority: 5},
			},
		},
	}}
	runner := survey.NewRunner(cat, survey.DefaultCaps())
	result, err := runner.Run(context.Background(), "ssot_drift", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	if result.ProbesRun != 2 {
		t.Fatalf("probes_run = %d want 2", result.ProbesRun)
	}
	if len(result.Ledger.Handles) == 0 {
		t.Fatal("expected candidate records")
	}
	if result.Digest == "" {
		t.Fatal("expected digest")
	}
	for _, rec := range result.Ledger.Handles {
		if !rec.Survey {
			t.Fatalf("record %q not survey-grade", rec.Handle)
		}
	}
}

func TestRunnerResolveEvidence(t *testing.T) {
	dir := writeFixtureRepo(t)
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"t": {
			ID: "t",
			Probes: []survey.Probe{{
				Kind: survey.ProbeGrep, Pattern: "Getenv", Path: ".", Label: "env", Priority: 1,
			}},
		},
	}}
	result, err := survey.NewRunner(cat, survey.DefaultCaps()).Run(context.Background(), "t", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	var targetPath string
	for _, rec := range result.Ledger.Handles {
		if rec.Path != "" {
			targetPath = rec.Path
			break
		}
	}
	if targetPath == "" {
		t.Fatal("no path in ledger")
	}
	res := evidence.Resolve(evidence.CitationRoots{ProjectDir: dir}, evidence.Triple{
		Path:    targetPath,
		Line:    1,
		Excerpt: "Getenv",
	}, result.Ledger, "")
	if res.Verdict == evidence.VerdictUnverifiable {
		t.Fatalf("resolve verdict = %q", res.Verdict)
	}
}

func TestRunnerPathEscape(t *testing.T) {
	dir := writeFixtureRepo(t)
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"t": {ID: "t", Probes: []survey.Probe{{Kind: survey.ProbeGrep, Pattern: "x", Label: "p", Priority: 1}}},
	}}
	_, err := survey.NewRunner(cat, survey.DefaultCaps()).Run(context.Background(), "t", "../outside", testScope(t, dir))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestRunnerProbeBatchBoundPreservesEveryProbe(t *testing.T) {
	dir := writeFixtureRepo(t)
	probes := []survey.Probe{
		{Kind: survey.ProbeGrep, Pattern: "Getenv", Label: "high", Priority: 10},
		{Kind: survey.ProbeGrep, Pattern: "/v1/", Label: "low", Priority: 1},
	}
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"t": {ID: "t", Probes: probes},
	}}
	caps := survey.DefaultCaps()
	caps.MaxProbeBatch = 1
	result, err := survey.NewRunner(cat, caps).Run(context.Background(), "t", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	if result.ProbesRun != 2 {
		t.Fatalf("probes_run = %d want 2", result.ProbesRun)
	}
}

func TestRunnerBatchProbeFailureIsolation(t *testing.T) {
	dir := writeFixtureRepo(t)
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"t": {ID: "t", Probes: []survey.Probe{
			{Kind: survey.ProbeGrep, Pattern: "Getenv", Label: "ok", Priority: 10},
			{Kind: survey.ProbeGrep, Pattern: "(?", Label: "bad", Priority: 1},
		}},
	}}
	result, err := survey.NewRunner(cat, survey.DefaultCaps()).Run(context.Background(), "t", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	if result.ProbesRun != 1 {
		t.Fatalf("probes_run = %d want 1", result.ProbesRun)
	}
	var sawErr bool
	for _, s := range result.ProbeSummaries {
		if s.Err != "" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected probe error summary")
	}
}

func TestRunnerEmitCapRollup(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	body.WriteString("package routes\n")
	for i := 0; i < 8; i++ {
		body.WriteString(`const path` + string(rune('A'+i)) + ` = "/v1/item` + string(rune('0'+i)) + "\"\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "routes.go"), []byte(body.String()), 0o644); err != nil {
		testutil.FailErr(t, "write routes", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package other\nconst x = \"/v1/only\"\n"), 0o644); err != nil {
		testutil.FailErr(t, "write other", err)
	}
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"api_routes": {
			ID: "api_routes",
			Probes: []survey.Probe{{
				Kind: survey.ProbeGrep, Pattern: `"/v1/`, Path: ".", Label: "http_path_literals",
				Priority: 10, EmitCap: 1,
			}},
		},
	}}
	result, err := survey.NewRunner(cat, survey.DefaultCaps()).Run(context.Background(), "api_routes", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	if len(result.ProbeSummaries) != 1 {
		t.Fatalf("summaries = %d", len(result.ProbeSummaries))
	}
	sum := result.ProbeSummaries[0]
	if !sum.Rolled {
		t.Fatalf("expected rolled summary: %+v", sum)
	}
	if sum.Emitted != 1 {
		t.Fatalf("emitted = %d want 1", sum.Emitted)
	}
	if len(result.Ledger.Handles) != 1 {
		t.Fatalf("ledger size = %d want 1", len(result.Ledger.Handles))
	}
	var rec evidence.Record
	for _, r := range result.Ledger.Handles {
		rec = r
		break
	}
	if rec.Kind != "rollup" {
		t.Fatalf("kind = %q want rollup", rec.Kind)
	}
	if rec.Path != "routes.go" {
		t.Fatalf("path = %q want routes.go (highest hit count)", rec.Path)
	}
	if !strings.Contains(result.Digest, "shape:") {
		t.Fatalf("digest missing shape: %s", result.Digest)
	}
	if !strings.Contains(result.Digest, "drill:") {
		t.Fatalf("digest missing drill: %s", result.Digest)
	}
}

func TestRunnerGrepCapRetainsFullScopeCount(t *testing.T) {
	dir := t.TempDir()
	for i := range 100 {
		name := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		if err := os.WriteFile(name, []byte("needle\n"), 0o644); err != nil {
			testutil.FailErr(t, "write grep fixture", err)
		}
	}
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"coverage": {ID: "coverage", Probes: []survey.Probe{{
			Kind: survey.ProbeGrep, Pattern: "needle", Path: ".", Label: "needle", Priority: 10,
		}}},
	}}
	caps := survey.DefaultCaps()
	caps.GrepMatchCap = 10
	result, err := survey.NewRunner(cat, caps).Run(context.Background(), "coverage", ".", testScope(t, dir))
	testutil.FailErr(t, "Run", err)
	if len(result.ProbeSummaries) != 1 {
		t.Fatalf("summaries = %#v", result.ProbeSummaries)
	}
	summary := result.ProbeSummaries[0]
	if summary.MatchCount != 100 || summary.SampleCount != 10 || !summary.Sampled || summary.FilesExamined != 100 {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(result.Digest, "full candidate pass; representative sample=10") {
		t.Fatalf("digest = %s", result.Digest)
	}
}
