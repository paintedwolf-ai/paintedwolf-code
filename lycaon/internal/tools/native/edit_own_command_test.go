package native

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
			err := EditMiss(context.Background(), provenanceCtx(dir, ledger), "main.go", filepath.Join(dir, "main.go"), "package main\n", "package moved", "package main")
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
