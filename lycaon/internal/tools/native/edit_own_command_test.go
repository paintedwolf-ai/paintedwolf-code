package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/toolsurface"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestACOC02EditMissOnOwnCommandEffectsIsAnOrdinaryMiss(t *testing.T) {
	own := sourceledger.Effect{Origin: api.SourceChangeOriginExternal, SessionID: "test-session", CommandWindowID: "window-1", Op: api.SourceChangeOpWrite, Ordinal: 9}
	person := sourceledger.Effect{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 8, TS: time.Date(2026, 8, 26, 14, 2, 0, 0, time.UTC)}
	otherWindow := sourceledger.Effect{Origin: api.SourceChangeOriginExternal, SessionID: "other-session", CommandWindowID: "window-2", Op: api.SourceChangeOpWrite, Ordinal: 7}
	for _, tc := range []struct {
		name      string
		effects   []sourceledger.Effect
		wantCode  string
		wantCount int
	}{
		{"only the session's own command", []sourceledger.Effect{own}, "EDIT_OLD_STRING_NOT_FOUND", 0},
		{"own command and a person", []sourceledger.Effect{own, person}, "EDIT_TARGET_CHANGED_BY_OTHERS", 1},
		{"another session's command", []sourceledger.Effect{otherWindow}, "EDIT_TARGET_CHANGED_BY_OTHERS", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
			ledger := &fakeSourceLedger{
				floor: 1, floorFound: true,
				heads:   map[string]sourceledger.BranchHead{"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"}},
				effects: map[string][]sourceledger.Effect{"f1": tc.effects},
			}
			edit := &EditTool{Boundary: nativefixture.Boundary(t)}
			_, err := edit.Run(context.Background(), map[string]any{
				"path": "main.go", "old_string": "package moved", "new_string": "package main",
			}, provenanceCtx(dir, ledger))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != tc.wantCode {
				t.Fatalf("edit miss = %v, want %s", err, tc.wantCode)
			}
			if tc.wantCount > 0 && reject.Data["change_count"] != tc.wantCount {
				t.Fatalf("change_count = %v, want %d", reject.Data["change_count"], tc.wantCount)
			}
		})
	}
}

// Offered means this turn's plan: a profile can hold a tool the turn's surface
// does not offer, and a deferred tool is still reachable.
func TestEditMissStatesWhetherThePlanOffersSourceHistory(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{
		{ID: "holds", Tools: map[string]bool{"edit": true, "source_history": true}},
	})
	plans := []struct {
		name string
		plan toolsurface.Plan
		want bool
	}{
		{"immediate", toolsurface.Compile([]string{"edit", "source_history"}, nil), true},
		{"deferred", toolsurface.Compile([]string{"edit"}, []string{"source_history"}), true},
		{"excluded", toolsurface.Compile([]string{"edit"}, nil), false},
	}
	misses := []struct {
		code    string
		effects []sourceledger.Effect
	}{
		{"EDIT_TARGET_CHANGED_BY_OTHERS", []sourceledger.Effect{{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 8}}},
		{"EDIT_OLD_STRING_NOT_FOUND", nil},
	}
	for _, plan := range plans {
		for _, miss := range misses {
			t.Run(plan.name+" "+miss.code, func(t *testing.T) {
				dir := t.TempDir()
				_, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
				ledger := &fakeSourceLedger{
					floor: 1, floorFound: true,
					heads:   map[string]sourceledger.BranchHead{"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"}},
					effects: map[string][]sourceledger.Effect{"f1": miss.effects},
				}
				tctx := provenanceCtx(dir, ledger)
				tctx.Identity.Agent = "holds"
				tctx.Turn.TurnToolPlan = plan.plan
				_, err := (&EditTool{Boundary: boundary}).Run(context.Background(), map[string]any{
					"path": "main.go", "old_string": "package moved", "new_string": "package main",
				}, tctx)
				var reject *toolrejection.ToolReject
				if !errors.As(err, &reject) || reject.Code != miss.code || reject.Data["source_history_offered"] != plan.want {
					t.Fatalf("edit miss = %v, want %s with source_history_offered %v", err, miss.code, plan.want)
				}
			})
		}
	}
}
