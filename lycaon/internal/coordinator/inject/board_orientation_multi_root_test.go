package inject_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildBoardInjectData_MultiRootSections(t *testing.T) {
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{FileCount: 10, Languages: []string{"Go"}},
		OrientationRoots: []api.BoardOrientationRoot{
			{Label: "lycaon", IsPrimary: true, Brief: api.RepoBrief{FileCount: 10, Languages: []string{"Go"}}},
			{Label: "den", Brief: api.RepoBrief{FileCount: 3, Languages: []string{"TypeScript"}}},
		},
	}
	data := inject.BuildBoardInjectData(snap, packboard.InjectScopeFull, false, false, time.Now().UTC())
	if !data.MultiRoot || data.RootCount != 2 || len(data.OrientationRoots) != 2 {
		t.Fatalf("data = %+v", data)
	}
}

func TestRenderBoardOrientationInject_MultiRootNamesBothRoots(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{FileCount: 10, Languages: []string{"Go"}, Layout: api.RepoLayout{TopLevel: []string{"lycaon/"}}},
		OrientationRoots: []api.BoardOrientationRoot{
			{Label: "lycaon", IsPrimary: true, Brief: api.RepoBrief{FileCount: 10, Languages: []string{"Go"}, Layout: api.RepoLayout{TopLevel: []string{"lycaon/"}}}},
			{Label: "den", Brief: api.RepoBrief{FileCount: 3, Languages: []string{"TypeScript"}, Layout: api.RepoLayout{TopLevel: []string{"src/"}}}},
		},
	}
	block, err := inject.RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", snap, packboard.InjectScopeFull, false, false, time.Now().UTC())
	testutil.FailErr(t, "RenderBoardOrientationInject", err)
	for _, want := range []string{"lycaon (primary)", "@den", "spans 2 folders", "@<label>/"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

func TestRenderBoardOrientationInject_SingleRootParityNoHeaders(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	snap := api.BoardSnapshot{
		Repo: api.RepoBrief{FileCount: 5, Languages: []string{"Go"}},
	}
	block, err := inject.RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", snap, packboard.InjectScopeFull, false, false, time.Now().UTC())
	testutil.FailErr(t, "RenderBoardOrientationInject", err)
	if strings.Contains(block, "spans ") || strings.Contains(block, "(primary)") {
		t.Fatalf("single-root inject must not include multi-root headers: %q", block)
	}
}

func TestRenderBoardOrientationInject_MultiRootBudgetIsDisclosed(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	roots := make([]api.BoardOrientationRoot, 40)
	for i := range roots {
		roots[i] = api.BoardOrientationRoot{
			Label:     "folder",
			Truncated: true,
			Brief:     api.RepoBrief{FileCount: 100, Languages: []string{strings.Repeat("x", 40)}},
		}
	}
	block, err := inject.RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", api.BoardSnapshot{OrientationRoots: roots}, packboard.InjectScopeFull, false, false, time.Now().UTC())
	testutil.FailErr(t, "RenderBoardOrientationInject", err)
	packStart := strings.Index(block, packboard.PackBoardSentinel)
	if packStart < 0 {
		t.Fatal("missing pack-board sentinel")
	}
	packBody := strings.TrimSpace(block[packStart+len(packboard.PackBoardSentinel):])
	if len(packBody) > api.MaxBoardInjectChars {
		t.Fatalf("pack body len %d exceeds %d: %q", len(packBody), api.MaxBoardInjectChars, packBody)
	}
	if !strings.Contains(block, "folders omitted") {
		t.Fatalf("missing omission disclosure: %q", block)
	}
}
