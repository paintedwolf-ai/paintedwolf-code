//go:build integration

package survey

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func integrationSummarizeTool(t *testing.T, dir string) *SummarizeTool {
	t.Helper()
	return &SummarizeTool{
		Boundary: nativefixture.Boundary(t),
		Caps:     summarize.DefaultCaps(),
	}
}

func seedGoFiles(t *testing.T, dir, prefix string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		writeFile(t, dir, fmt.Sprintf("%s/f%d.go", prefix, i), "package main\n// seed line\n")
	}
}

func TestSummarizeIntegrationPerStrategy(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")

	cases := []struct {
		name string
		seed func(dir string)
		args map[string]any
	}{
		{
			name: "single inline",
			seed: func(string) {},
			args: map[string]any{
				"task":    "explain",
				"content": strings.Repeat("package inline\n", 30),
			},
		},
		{
			name: "single file",
			seed: func(dir string) { writeFile(t, dir, "one/a.go", "package main\n") },
			args: map[string]any{"task": "explain", "path": "one/a.go"},
		},
		{
			name: "structure dir",
			seed: func(dir string) { seedGoFiles(t, dir, "sel", 40) },
			args: map[string]any{"task": "explain", "path": "sel"},
		},
		{
			name: "structure pattern",
			seed: func(dir string) { seedGoFiles(t, dir, "wide", 80) },
			args: map[string]any{"task": "explain", "path": "wide", "pattern": "package"},
		},
		{
			name: "large dir pack",
			seed: func(dir string) { seedGoFiles(t, dir, "tree", 70) },
			args: map[string]any{"task": "what lives here", "path": "tree"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.seed(dir)
			tool := integrationSummarizeTool(t, dir)

			raw, err := tool.Run(context.Background(), tc.args, nativefixture.Context(dir))
			testutil.FailErr(t, "run", err)
			resp := decodeSummarizeResponse(t, raw)

			if len(resp.Pack.Identity) == 0 && len(resp.Pack.Skeleton) == 0 && len(resp.Pack.Substance) == 0 {
				t.Fatalf("pack is empty: %+v", resp.Pack)
			}
		})
	}
}
