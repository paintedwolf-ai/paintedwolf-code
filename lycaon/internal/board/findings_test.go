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
		raw, err := readFindings(t.Context(), map[string]any{"findings_after": after}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "child"},
		}, deps)
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
	raw, err := readFindings(t.Context(), map[string]any{"finding_id": int64(1)}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child"},
	}, deps)
	testutil.FailErr(t, "read finding body", err)
	if !strings.Contains(raw, "large private detail") {
		t.Fatal("detail missing")
	}
	if _, err := readFindings(t.Context(), map[string]any{"finding_id": int64(20)}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child"},
	}, deps); err == nil {
		t.Fatal("read another root's finding")
	}
}

func TestReadFindingsToolRegistrationAndValidation(t *testing.T) {
	store := findings.NewMemoryStore()
	_, err := store.Append(t.Context(), "root", "scout", "note-1", "a.go", "full body")
	testutil.FailErr(t, "append finding", err)

	reg := tools.NewDefaultRegistry()
	deps := ToolDeps{
		Builder:     &SnapshotBuilder{},
		Findings:    func() findings.Store { return store },
		RootSession: func(_ context.Context, s string) string { return s },
	}
	testutil.FailErr(t, "register board tools", RegisterBoardTools(reg, deps))

	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "root"}}
	raw, err := reg.Run(t.Context(), "read_findings", map[string]any{"finding_id": float64(1)}, tctx)
	testutil.FailErr(t, "run read_findings by id", err)
	var item api.Finding
	testutil.FailErr(t, "decode finding", json.Unmarshal([]byte(raw), &item))
	if item.ID != 1 || item.Body != "full body" {
		t.Fatalf("finding = %+v", item)
	}

	invalidCases := []map[string]any{
		{},
		{"finding_id": 1, "findings_after": 0},
		{"finding_id": 1, "extra": true},
		{"finding_id": 0},
		{"finding_id": -1},
		{"findings_after": "bad"},
		{"finding_id": 999},
	}
	for i, args := range invalidCases {
		if _, err := reg.Run(t.Context(), "read_findings", args, tctx); err == nil {
			t.Fatalf("case %d (%v) expected rejection", i, args)
		}
	}

	if _, err := readFindings(t.Context(), map[string]any{"findings_after": 0}, tctx, ToolDeps{}); err == nil {
		t.Fatal("expected error when findings store is nil")
	}
	if _, err := readFindings(t.Context(), map[string]any{"findings_after": 0}, tools.ToolContext{}, deps); err == nil {
		t.Fatal("expected error when root session is empty")
	}
}
