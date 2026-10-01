package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/jq"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDiffPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	diffTool := &surveytools.DiffTool{Boundary: b}
	_, err := diffTool.Run(context.Background(), map[string]any{
		"path_a": "../outside.go",
		"path_b": "inside.go",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestDiffOversizedFileBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	big := strings.Repeat("x", (2<<20)+1)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		testutil.FailErr(t, "write big", err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("a\n"), 0o644); err != nil {
		testutil.FailErr(t, "write small", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	diffTool := &surveytools.DiffTool{Boundary: b}
	_, err := diffTool.Run(context.Background(), map[string]any{
		"path_a": "big.txt",
		"path_b": "small.txt",
	}, implementToolContext(root))
	if err == nil {
		t.Fatal("expected oversized file rejection")
	}
}

func TestDiffOutputTruncatedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var oldB, newB strings.Builder
	for i := range 5000 {
		line := strings.Repeat("y", 80)
		oldB.WriteString(line)
		oldB.WriteByte('\n')
		newB.WriteString(line)
		newB.WriteByte('\n')
		if i%2 == 0 {
			newB.WriteString("delta\n")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(oldB.String()), 0o644); err != nil {
		testutil.FailErr(t, "write a", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte(newB.String()), 0o644); err != nil {
		testutil.FailErr(t, "write b", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	diffTool := &surveytools.DiffTool{Boundary: b}
	out, err := diffTool.Run(context.Background(), map[string]any{
		"path_a": "a.txt",
		"path_b": "b.txt",
	}, implementToolContext(root))
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("expected truncated diff output")
	}
}

func runJqSecurity(t *testing.T, b *sandbox.Boundary, root string, args map[string]any) (string, error) {
	t.Helper()
	tool := &jq.Tool{Boundary: b}
	return tool.Run(context.Background(), args, implementToolContext(root))
}

func TestJqPathEscapeBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	b := contractcheck.ProdToolBoundary(t)
	_, err := runJqSecurity(t, b, root, map[string]any{
		"path":  "../outside.json",
		"query": ".a",
	})
	if err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestJqOversizedFileBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	big := strings.Repeat("x", (20<<20)+1)
	if err := os.WriteFile(filepath.Join(root, "big.json"), []byte(big), 0o644); err != nil {
		testutil.FailErr(t, "write big", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	_, err := runJqSecurity(t, b, root, map[string]any{
		"path":  "big.json",
		"query": ".a",
	})
	if err == nil {
		t.Fatal("expected oversized file rejection")
	}
}

func TestJqDocumentDepthBlockedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("root:\n")
	for i := 0; i < 40; i++ {
		b.WriteString(strings.Repeat("  ", i+1))
		b.WriteString("child:\n")
	}
	b.WriteString(strings.Repeat("  ", 41))
	b.WriteString("value: 1\n")
	if err := os.WriteFile(filepath.Join(root, "deep.yaml"), []byte(b.String()), 0o644); err != nil {
		testutil.FailErr(t, "write deep yaml", err)
	}
	bnd := contractcheck.ProdToolBoundary(t)
	_, err := runJqSecurity(t, bnd, root, map[string]any{
		"path":   "deep.yaml",
		"format": "yaml",
		"query":  ".root",
	})
	if err == nil {
		t.Fatal("expected document depth rejection")
	}
}

func TestJqResultTruncatedSecurity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	items := make([]string, 0, 500)
	for range 500 {
		items = append(items, strings.Repeat("z", 200))
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		testutil.FailErr(t, "marshal", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.json"), raw, 0o644); err != nil {
		testutil.FailErr(t, "write data", err)
	}
	b := contractcheck.ProdToolBoundary(t)
	out, err := runJqSecurity(t, b, root, map[string]any{
		"path":  "data.json",
		"query": ".items[]",
	})
	if err != nil {
		t.Fatalf("jq: %v", err)
	}
	if !strings.Contains(out, `"truncated":true`) && !strings.Contains(out, `"shape"`) {
		t.Fatalf("expected truncated or zoomed jq output")
	}
}
