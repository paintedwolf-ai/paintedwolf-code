package board

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindingPagesAndDetailsAreRootScoped(t *testing.T) {
	store := findings.NewMemoryStore()
	for i := 0; i < 19; i++ {
		_, err := store.Append(t.Context(), "root", "peer", fmt.Sprint(i), "source.go", "large private detail")
		testutil.FailErr(t, "append finding", err)
	}
	_, err := store.Append(t.Context(), "other", "peer", "unrelated", "other.go", "unrelated detail")
	testutil.FailErr(t, "append unrelated finding", err)
	deps := ToolDeps{Findings: func() findings.Store { return store }, RootSession: func(context.Context, string) string { return "root" }}
	after := int64(0)
	total := 0
	for {
		raw, err := readFindings(t.Context(), map[string]any{"findings_after": after}, tools.ToolContext{SessionID: "child"}, deps)
		testutil.FailErr(t, "page findings", err)
		if strings.Contains(raw, "large private detail") || strings.Contains(raw, "unrelated") {
			t.Fatal("page exposed body or another root")
		}
		var page struct {
			Findings []api.Finding `json:"findings"`
			Next     int64         `json:"next_after"`
			More     bool          `json:"has_more"`
		}
		testutil.FailErr(t, "decode page", json.Unmarshal([]byte(raw), &page))
		total += len(page.Findings)
		if len(page.Findings) > 0 && !page.Findings[0].HasBody {
			t.Fatal("page lost body availability")
		}
		after = page.Next
		if !page.More {
			break
		}
	}
	if total != 19 {
		t.Fatalf("paged %d findings, want 19", total)
	}
	raw, err := readFindings(t.Context(), map[string]any{"finding_id": int64(1)}, tools.ToolContext{SessionID: "child"}, deps)
	testutil.FailErr(t, "read finding body", err)
	if !strings.Contains(raw, "large private detail") {
		t.Fatal("detail missing")
	}
	if _, err := readFindings(t.Context(), map[string]any{"finding_id": int64(20)}, tools.ToolContext{SessionID: "child"}, deps); err == nil {
		t.Fatal("read another root's finding")
	}
}
