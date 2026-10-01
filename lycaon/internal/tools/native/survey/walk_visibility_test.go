package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// Project ignore rules do not hide files from survey tools.
func TestGrepFindsGitignoredFile(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write .gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.env\n"), 0o644))
	testutil.FailErr(t, "write ignored", os.WriteFile(filepath.Join(dir, "secret.env"), []byte("TOKEN=abc123\n"), 0o644))
	testutil.FailErr(t, "write tracked", os.WriteFile(filepath.Join(dir, "config.go"), []byte("// TOKEN=abc123\n"), 0o644))

	out, err := (&GrepTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"pattern": "TOKEN=abc123"}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	paths := map[string]bool{}
	for _, m := range nativefixture.GrepMatches(t, out) {
		paths[m["path"].(string)] = true
	}
	if !paths["secret.env"] || !paths["config.go"] {
		t.Fatalf("grep should surface the gitignored file too, got %v", paths)
	}
}

func TestFindReturnsExplicitGitignoredPath(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write .gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("generated.go\n"), 0o644))
	testutil.FailErr(t, "write ignored", os.WriteFile(filepath.Join(dir, "generated.go"), []byte("package x\n"), 0o644))

	out, err := (&FindTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"path": "generated.go", "type": "file"}, nativefixture.Context(dir))
	testutil.FailErr(t, "find", err)
	results := surveyFindResults(t, out)
	if len(results) != 1 {
		t.Fatalf("explicit gitignored path should resolve, got %+v", results)
	}
}

func TestGrepStillSkipsEngineAndVCSMetadata(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git", "objects")
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(gitDir, 0o755))
	testutil.FailErr(t, "write git object", os.WriteFile(filepath.Join(gitDir, "pack"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write src", os.WriteFile(filepath.Join(dir, "main.go"), []byte("// needle\n"), 0o644))

	out, err := (&GrepTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	paths := map[string]bool{}
	for _, m := range nativefixture.GrepMatches(t, out) {
		paths[m["path"].(string)] = true
	}
	if paths[".git/objects/pack"] {
		t.Fatalf(".git must stay pruned, got %+v", paths)
	}
	if !paths["main.go"] {
		t.Fatalf("expected product match, got %+v", paths)
	}
}

func TestGrepSurfacesNodeModulesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules", "pkg")
	testutil.FailErr(t, "mkdir node_modules", os.MkdirAll(nm, 0o755))
	testutil.FailErr(t, "write dep", os.WriteFile(filepath.Join(nm, "index.js"), []byte("needle\n"), 0o644))
	testutil.FailErr(t, "write src", os.WriteFile(filepath.Join(dir, "main.go"), []byte("// quiet\n"), 0o644))

	out, err := (&GrepTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"pattern": "needle"}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	found := false
	for _, m := range nativefixture.GrepMatches(t, out) {
		if strings.Contains(m["path"].(string), "node_modules") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("node_modules must remain reachable without a stack-specific skip list")
	}
}

// On truncation grep reports where the match cap was spent, so the agent can
// scope past a noisy directory instead of paging through it.
func TestGrepDistributionOnTruncation(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"aaa", "zzz"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, sub), 0o755))
		for i := 0; i < 3; i++ {
			name := filepath.Join(dir, sub, string(rune('a'+i))+".go")
			testutil.FailErr(t, "write", os.WriteFile(name, []byte("// needle\n"), 0o644))
		}
	}

	out, err := (&GrepTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{"pattern": "needle", "max_matches": float64(2)}, nativefixture.Context(dir))
	testutil.FailErr(t, "grep", err)
	var resp struct {
		Truncated    bool `json:"truncated"`
		Distribution []struct {
			Dir   string `json:"dir"`
			Count int    `json:"count"`
		} `json:"distribution"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp); err != nil {
		t.Fatalf("decode grep: %v (raw=%s)", err, out)
	}
	if !resp.Truncated || len(resp.Distribution) == 0 {
		t.Fatalf("expected truncation with a distribution, got %+v", resp)
	}
	if resp.Distribution[0].Dir != "aaa" {
		t.Fatalf("distribution should point at the directory that consumed the cap, got %+v", resp.Distribution)
	}
}

func surveyFindResults(t *testing.T, out string) []map[string]any {
	t.Helper()
	var wrap struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &wrap); err != nil {
		t.Fatalf("decode find: %v (raw=%s)", err, out)
	}
	return wrap.Results
}
