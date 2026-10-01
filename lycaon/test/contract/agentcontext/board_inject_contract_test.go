package contract

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBoardInjectServeWiring(t *testing.T) {
	t.Parallel()
	text := contractcheck.ServeWireSource(t)
	for _, needle := range []string{"SetBoardInject", "RegisterBoardTools", "InjectBuilder"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("serve wire missing %q board inject wiring", needle)
		}
	}
}

func fatLangSnapshot() api.BoardSnapshot {
	langs := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		langs = append(langs, strings.Repeat("x", 20))
	}
	return api.BoardSnapshot{
		Repo:        api.RepoBrief{Languages: langs, FileCount: 9999},
		DetailLevel: api.BoardDetailLevelCompact,
	}
}

func TestBoardInjectFormatterRespectsCharBudget(t *testing.T) {
	t.Parallel()
	f := board.DefaultInjectFormatter()
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	text, ok := f.FormatBoardInject(fatLangSnapshot(), false, now)
	if !ok {
		t.Fatal("expected inject output")
	}
	if len(text) > api.MaxBoardInjectChars+32 {
		t.Fatalf("inject output len = %d exceeds budget", len(text))
	}
}

func TestBoardInjectEnvelopeMarker(t *testing.T) {
	t.Parallel()
	f := board.DefaultInjectFormatter()
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	snap := api.BoardSnapshot{
		Repo:        api.RepoBrief{Languages: []string{"Go"}, FileCount: 12},
		DetailLevel: api.BoardDetailLevelCompact,
	}
	text, ok := f.FormatBoardInject(snap, false, now)
	if !ok {
		t.Fatal("expected inject output")
	}
	if !strings.Contains(text, "pack-board:v1") {
		t.Fatal("inject output missing pack-board:v1 envelope marker")
	}
}

func TestBoardOrientationInjectRenderRespectsCharBudget(t *testing.T) {
	t.Parallel()
	renderer := prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t))
	now := time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC)
	block, err := inject.RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", fatLangSnapshot(), packboard.InjectScopeFull, false, true, now)
	contractcheck.FailErr(t, "inject.RenderBoardOrientationInject failed", err)
	if !strings.Contains(block, inject.BoardOrientationInjectSentinel) {
		t.Fatal("missing lycaon-board-orientation sentinel")
	}
	packStart := strings.Index(block, packboard.PackBoardSentinel)
	if packStart < 0 {
		t.Fatal("missing pack-board:v1 envelope marker")
	}
	packBody := strings.TrimSpace(block[packStart+len(packboard.PackBoardSentinel):])
	if len(packBody) > api.MaxBoardInjectChars+4 {
		t.Fatalf("pack body len %d exceeds budget", len(packBody))
	}
}
