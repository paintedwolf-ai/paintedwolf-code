package native

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestMutationTimeoutPreservesFileAndReportsHostCause(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.SourceParsing: "version: 1\nvalidation_timeout_ms: 1\nanalysis_timeout_ms: 5000\n"})
	dir := t.TempDir()
	original := "echo original\n"
	path := filepath.Join(dir, "script.sh")
	testutil.FailErr(t, "seed source", os.WriteFile(path, []byte(original), 0600))
	proposed := "  log) printf '%s' x ;;\n" + strings.Repeat("  local va=\"${arr[@]:-}\"\n  if [[ -n \"${v}\" ]]; then printf '%s' \"${v}\"; fi\n", 160)
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "script.sh", "content": proposed}, nativefixture.Context(dir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "MUTATION_PARSE_INCOMPLETE" {
		t.Fatalf("write result = %v", err)
	}
	if reject.Data["parse_reason"] != "timeout" || reject.Data["parse_timeout_ms"] != int64(1) || reject.Data["parse_source_bytes"] != len(proposed) || reject.Data["parse_phase"] != "candidate" {
		t.Fatalf("incomplete diagnostics: %+v", reject.Data)
	}
	if _, ok := reject.Data["after_syntax_burden"]; ok {
		t.Fatalf("unavailable parse claimed a syntax burden: %+v", reject.Data)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read preserved source", err)
	if string(got) != original {
		t.Fatalf("rejected write changed source: %q", got)
	}
}

func TestOriginalParseFailureRetainsPhase(t *testing.T) {
	failure := &tsparse.Failure{Reason: "timeout", Language: "go", TimeoutMS: 30000, SourceBytes: 100}
	change := syntaxhealth.Change{Transition: syntaxhealth.TransitionParseIncomplete, Before: &syntaxhealth.Report{Status: syntaxhealth.StatusIncomplete, Failure: failure}, After: syntaxhealth.Report{
		Status: syntaxhealth.StatusBroken, Diagnostics: []syntaxhealth.Diagnostic{{Row: 5, Col: 12, Kind: "missing", NodeType: ")"}},
	}}
	data := syntaxRejectData("edit", "a.go", change, nil, mutationSeam{})
	if data["parse_phase"] != "original" || data["parse_source_bytes"] != 100 {
		t.Fatalf("wrong failing snapshot: %+v", data)
	}
	if descriptions, ok := data["parse_errors"].([]string); !ok || len(descriptions) != 1 || !strings.Contains(descriptions[0], "missing )") {
		t.Fatalf("completed candidate diagnostics lost: %+v", data)
	}
}

func TestCleanCandidateDoesNotRequireParsingExhaustedOriginal(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.SourceParsing: "version: 1\nvalidation_timeout_ms: 50\nanalysis_timeout_ms: 5000\n"})
	original := "  log) printf '%s' x ;;\n" + strings.Repeat("  local va=\"${arr[@]:-}\"\n  if [[ -n \"${v}\" ]]; then printf '%s' \"${v}\"; fi\n", 160)
	change := syntaxhealth.Evaluate(context.Background(), "script.sh", []byte(original), true, []byte("echo repaired\n"))
	if change.Transition != syntaxhealth.TransitionAllowed || change.Before != nil || change.After.Status != syntaxhealth.StatusClean {
		t.Fatalf("clean candidate required an original parse: %+v", change)
	}
}

func TestPatternParserFailureIsNotBadPattern(t *testing.T) {
	failure := &tsparse.Failure{Reason: "canceled", Language: "swift", TimeoutMS: 5000, SourceBytes: 30}
	reject := sourceview.ParseReject("Level.swift", "source", &structrewrite.PatternError{Cause: failure})
	if reject == nil || reject.Code != "SOURCE_PARSE_INCOMPLETE" || reject.Data["parse_phase"] != "pattern" {
		t.Fatalf("lost pattern parser failure: %+v", reject)
	}
}

func TestWriteSyntaxOverrideStillChecksContentAndRecordsReason(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	tc := nativefixture.Context(dir)
	tc.Out = &tools.ToolInvocationOut{}
	content := "package broken\nfunc ("
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "a.go", "content": content, "syntax_override_reason": "intentional incomplete edit",
	}, tc)
	testutil.FailErr(t, "write with explicit override", err)
	got, err := os.ReadFile(filepath.Join(dir, "a.go"))
	testutil.FailErr(t, "read overridden mutation", err)
	if string(got) != content || !tc.Out.Facts.HasCode(tools.SyntaxCheckOverriddenCode) {
		t.Fatalf("mutation or override record missing: %q %+v", got, tc.Out.Facts)
	}
	_, err = tool.Run(context.Background(), map[string]any{
		"path": "binary.go", "content": "package broken\x00", "syntax_override_reason": "intentional incomplete edit",
	}, nativefixture.Context(dir))
	if err == nil {
		t.Fatal("syntax override bypassed binary content protection")
	}
	if _, err := os.Stat(filepath.Join(dir, "binary.go")); !os.IsNotExist(err) {
		t.Fatalf("rejected binary content was written: %v", err)
	}
}

func TestRewritePreviewRetainsParserFailureDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := codeRewriteDiffResult(ctx, "a.go", "go", 1, "package p\n", "package q\n")
	var out codeRewriteDiffOut
	testutil.FailErr(t, "decode rewrite preview", json.Unmarshal([]byte(raw), &out))
	if out.SyntaxIssue == nil || out.SyntaxIssue.Code != "MUTATION_PARSE_INCOMPLETE" || out.SyntaxIssue.Details["parse_reason"] != "canceled" || out.SyntaxIssue.Details["parse_phase"] != "candidate" {
		t.Fatalf("preview discarded parser failure details: %+v", out)
	}
}

func TestMultiRewritePreviewRetainsSyntaxDiagnostics(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{"a.go", "b.go"} {
		testutil.FailErr(t, "seed rewrite source", os.WriteFile(filepath.Join(dir, path), []byte("package p\nfunc run() { f(1) }\n"), 0600))
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	raw, err := tool.Run(context.Background(), map[string]any{"paths": []string{"a.go", "b.go"}, "pattern": "f($A)", "rewrite": "broken(", "dry_run": true}, nativefixture.Context(dir))
	testutil.FailErr(t, "preview invalid structural rewrite", err)
	var out multiApplyOut
	testutil.FailErr(t, "decode multi-file rewrite preview", json.Unmarshal([]byte(raw), &out))
	if len(out.Changed) != 2 {
		t.Fatalf("preview lost changed files: %+v", out)
	}
	for _, file := range out.Changed {
		if file.SyntaxIssue == nil || file.SyntaxIssue.Code != "MUTATION_BROKE_PARSE" || file.SyntaxIssue.Details["parse_errors"] == nil {
			t.Fatalf("preview lost syntax diagnostics: %+v", file)
		}
	}
}

func TestMultiRewriteParseFailurePreservesPlannedFiles(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.SourceParsing: "version: 1\nvalidation_timeout_ms: 30000\nanalysis_timeout_ms: 20\n"})
	dir := t.TempDir()
	sources := map[string]string{
		"a.go": "package p\nfunc run() { f(1) }\n",
		"b.go": "package p\nfunc run() {\n" + strings.Repeat("f(1)\n", 40000) + "}\n",
	}
	for path, source := range sources {
		testutil.FailErr(t, "seed rewrite source", os.WriteFile(filepath.Join(dir, path), []byte(source), 0600))
	}
	tool := &CodeRewriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"paths": []string{"a.go", "b.go"}, "pattern": "f($A)", "rewrite": "g($A)"}, nativefixture.Context(dir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SOURCE_PARSE_INCOMPLETE" {
		t.Fatalf("unavailable rewrite source did not reject planning: %v", err)
	}
	for path, source := range sources {
		got, err := os.ReadFile(filepath.Join(dir, path))
		testutil.FailErr(t, "read preserved rewrite source", err)
		if string(got) != source {
			t.Fatalf("rewrite applied a partial plan to %s", path)
		}
	}
}
