package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/guidancescan"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// toolEnvelopeHintCodes are hint_code values carried inside successful tool JSON payloads.
var toolEnvelopeHintCodes = []string{
	"SCAN_LIST_EMPTY",
	"REPO_MAP_EMPTY",
	"TOOL_SURVEY_BYTE_CLAMPED",
}

func loadToolEnvelopeHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	return cfg
}

func TestToolEnvelopeHintCodesRegisteredAsBanner(t *testing.T) {
	t.Parallel()
	cfg := loadToolEnvelopeHintConfig(t)
	for _, code := range toolEnvelopeHintCodes {
		entry, ok := cfg.HintCodes[code]
		if !ok {
			t.Fatalf("hint registry missing envelope hint %q", code)
		}
		if entry.Emit != "banner" {
			t.Fatalf("hint %q emit=%q want banner", code, entry.Emit)
		}
		view := guidance.NormalizeRejectCodeView(code, entry)
		for _, field := range []struct{ name, val string }{
			{"message", entry.Message},
			{"what", view.What},
			{"fix", view.Fix},
			{"instead", view.Instead},
		} {
			if strings.TrimSpace(field.val) == "" {
				t.Fatalf("hint %q missing %s", code, field.name)
			}
		}
	}
}

func TestToolEnvelopeHintCodesHaveProductionEmission(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	registered := make(map[string]bool, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = true
	}
	scanResult, err := guidancescan.ScanGuidanceEmission(lycaonRoot)
	contractcheck.FailErr(t, "scan guidance emission", err)
	for _, code := range toolEnvelopeHintCodes {
		if !scanResult.HasEmissionSite(code) {
			t.Fatalf("tool envelope hint %q has no production emission site", code)
		}
	}
}

func TestToolEnvelopePackagesNoInlineHintProseConsts(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	sites, err := guidancescan.ScanInlineEnvelopeHintConsts(lycaonRoot)
	contractcheck.FailErr(t, "scan inline envelope hint consts", err)
	if len(sites) > 0 {
		t.Fatalf("tool envelope packages must not define inline Hint prose consts (use hint registry + BundledEnvelopeHint):\n%s",
			strings.Join(sites, "\n"))
	}
}

func TestListScansEnvelopeNeverBareArray(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(scantoolapi.NewListScansResponse(t.Context(), nil))
	contractcheck.FailErr(t, "marshal list_scans", err)
	var top json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("top-level must be JSON object, got %q", string(raw))
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatalf("list_scans must never return bare array: %s", raw)
	}
	var obj map[string]any
	contractcheck.FailErr(t, "unmarshal envelope", json.Unmarshal(raw, &obj))
	for _, key := range []string{"scans", "count"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("list_scans envelope missing %q: %s", key, raw)
		}
	}
}

func TestListScansEmptyHintMatchesRegistry(t *testing.T) {
	t.Parallel()
	cfg := loadToolEnvelopeHintConfig(t)
	resp := scantoolapi.NewListScansResponse(t.Context(), nil)
	if resp.HintCode != "SCAN_LIST_EMPTY" {
		t.Fatalf("HintCode = %q", resp.HintCode)
	}
	want := guidance.EnvelopeHintMessage(t.Context(), cfg, resp.HintCode, nil)
	if resp.Hint != want {
		t.Fatalf("Hint = %q want registry message %q", resp.Hint, want)
	}
}

func TestGitStatusWireJSONUsesSnakeCase(t *testing.T) {
	t.Parallel()
	status := &git.GitStatus{
		Branch:        "main",
		Dirty:         true,
		StagedCount:   1,
		UnstagedCount: 2,
		RecentCommits: []string{"Initial commit"},
	}
	raw, err := json.Marshal(git.StatusToolResponse{
		Available:     true,
		Branch:        status.Branch,
		Dirty:         status.Dirty,
		StagedCount:   status.StagedCount,
		UnstagedCount: status.UnstagedCount,
		RecentCommits: status.RecentCommits,
	})
	contractcheck.FailErr(t, "marshal git status", err)
	var obj map[string]any
	contractcheck.FailErr(t, "unmarshal git status", json.Unmarshal(raw, &obj))
	for key := range obj {
		if key != strings.ToLower(key) {
			t.Fatalf("git_status wire key %q must be snake_case", key)
		}
	}
	for _, want := range []string{"available", "branch", "dirty", "staged_count", "unstaged_count", "recent_commits"} {
		if _, ok := obj[want]; !ok {
			t.Fatalf("git_status wire missing %q: %s", want, raw)
		}
	}
}

func TestGitStatusToolIncludesRecentCommits(t *testing.T) {
	tmpDir := t.TempDir()
	contractcheck.FailErr(t, "write tracked", os.WriteFile(filepath.Join(tmpDir, "tracked.txt"), []byte("x"), 0o644))
	gittest.InitCommit(t, tmpDir, "Initial commit")
	execTool := toolfixture.ContractToolExecutor(t)
	out, err := execTool.Invoke(context.Background(), "git_status", map[string]any{}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmpDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	contractcheck.FailErr(t, "git_status invoke", err)
	var obj map[string]any
	contractcheck.FailErr(t, "decode git_status", json.Unmarshal([]byte(out), &obj))
	commits, ok := obj["recent_commits"].([]any)
	if !ok {
		t.Fatalf("recent_commits missing or wrong type: %s", out)
	}
	if len(commits) == 0 {
		t.Fatalf("expected recent_commits entries: %s", out)
	}
	if obj["available"] != true {
		t.Fatalf("expected available=true: %s", out)
	}
}

func TestFindToolWireDepthSemantics(t *testing.T) {
	tmpDir := t.TempDir()
	deep := filepath.Join(tmpDir, "a", "b", "c", "d", "e", "f", "g", "h", "i")
	contractcheck.FailErr(t, "mkdir a", os.MkdirAll(filepath.Join(tmpDir, "a"), 0o755))
	contractcheck.FailErr(t, "write shallow", os.WriteFile(filepath.Join(tmpDir, "a", "shallow.go"), []byte("x"), 0o644))
	contractcheck.FailErr(t, "mkdir deep", os.MkdirAll(deep, 0o755))
	contractcheck.FailErr(t, "write deep", os.WriteFile(filepath.Join(deep, "deep.go"), []byte("x"), 0o644))

	execTool := toolfixture.ContractToolExecutor(t)
	out, err := execTool.Invoke(context.Background(), "find", map[string]any{
		"path":      ".",
		"type":      "file",
		"name_glob": "**/*.go",
		"max_depth": float64(3),
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmpDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	contractcheck.FailErr(t, "find invoke", err)
	if strings.Contains(out, "depth_clipped") {
		t.Fatalf("find output must not use depth_clipped: %s", out)
	}
	var obj map[string]any
	contractcheck.FailErr(t, "decode find", json.Unmarshal([]byte(out), &obj))
	if obj["deeper_paths_omitted"] != true {
		t.Fatalf("deeper_paths_omitted = %v want true: %s", obj["deeper_paths_omitted"], out)
	}
	notice, _ := obj["depth_notice"].(string)
	if strings.TrimSpace(notice) == "" {
		t.Fatalf("depth_notice missing: %s", out)
	}
}

func TestReadToolWireIncludesLineTotal(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sample.txt")
	content := "alpha\nbeta\ngamma\n"
	contractcheck.FailErr(t, "write sample", os.WriteFile(path, []byte(content), 0o644))

	execTool := toolfixture.ContractToolExecutor(t)
	out, err := execTool.Invoke(context.Background(), "read", map[string]any{
		"path": "sample.txt",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmpDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	contractcheck.FailErr(t, "read invoke", err)
	var obj map[string]any
	contractcheck.FailErr(t, "decode read", json.Unmarshal([]byte(out), &obj))
	if obj["total_lines"].(float64) != 3 {
		t.Fatalf("total_lines = %v want 3: %s", obj["total_lines"], out)
	}
}

func TestClampSessionToolOutputContractPreservesValidJSON(t *testing.T) {
	t.Parallel()
	results := make([]map[string]any, 0, 80)
	for i := 0; i < 80; i++ {
		results = append(results, map[string]any{
			"path": strings.Repeat("p", 96) + "/file.go",
			"type": "file",
		})
	}
	payload, err := json.Marshal(map[string]any{
		"results":     results,
		"offset":      100,
		"truncated":   false,
		"next_offset": 180,
		"receipt":     surveyreceipt.New("find", ".", 80, 0, false),
	})
	contractcheck.FailErr(t, "marshal find payload", err)
	clamp, ok := surveyreceipt.ClampSessionToolOutput(string(payload), 8192)
	if !ok {
		t.Fatalf("expected clamp for payload len=%d", len(payload))
	}
	clamped := clamp.Output
	if strings.Contains(clamped, "...[truncated]") {
		t.Fatalf("clamp must not break JSON with spill suffix: %s", clamped)
	}
	var obj map[string]any
	contractcheck.FailErr(t, "decode clamped json", json.Unmarshal([]byte(clamped), &obj))
	page, _ := obj["results"].([]any)
	if len(page) == 0 || len(page) >= 80 {
		t.Fatalf("results len = %d want partial page", len(page))
	}
	if obj["truncated"] != true {
		t.Fatalf("truncated = %v", obj["truncated"])
	}
	next, _ := obj["next_offset"].(float64)
	if int(next) != 100+len(page) {
		t.Fatalf("next_offset = %v want %d", obj["next_offset"], 100+len(page))
	}
	banner, _ := obj["truncation_banner"].(string)
	if banner != "TOOL_SURVEY_BYTE_CLAMPED" {
		t.Fatalf("truncation_banner = %q want registered code", banner)
	}
}

func TestSurveyByteClampUsesRegisteredHintCode(t *testing.T) {
	t.Parallel()
	cfg := loadToolEnvelopeHintConfig(t)
	clamped := `{"results":[],"offset":0,"truncated":true,"next_offset":0,"truncation_banner":"trimmed"}`
	vars := map[string]any{"kept": 0, "page_key": "results", "offset": 0, "next_offset": 0}
	msg := guidance.EnvelopeHintMessage(t.Context(), cfg, "TOOL_SURVEY_BYTE_CLAMPED", vars)
	out := guidance.AppendOutputBanner(clamped, "TOOL_SURVEY_BYTE_CLAMPED", msg)
	if !strings.Contains(out, "Code: TOOL_SURVEY_BYTE_CLAMPED") {
		t.Fatalf("missing registered banner code: %q", out)
	}
	if strings.Contains(msg, "{{") {
		t.Fatalf("hint message must render template vars: %q", msg)
	}
	if !strings.Contains(out, clamped) {
		t.Fatal("banner must preserve clamped JSON prefix")
	}
}

func TestSurveyreceiptNoForbiddenInlineSessionCapProse(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "surveyreceipt")
	entries, err := os.ReadDir(path)
	contractcheck.FailErr(t, "read surveyreceipt dir", err)
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, ent.Name()))
		contractcheck.FailErr(t, "read file", err)
		if strings.Contains(string(data), "session byte cap") {
			t.Fatalf("%s must not contain inline session byte cap prose (use TOOL_SURVEY_BYTE_CLAMPED registry)", ent.Name())
		}
	}
}
