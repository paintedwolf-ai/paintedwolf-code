package board

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func marshalBoardView(t *testing.T, snap *api.BoardSnapshot, level api.BoardDetailLevel, now time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(BuildBoardView(snap, "sess-1", level, now))
	testutil.FailErr(t, "json.Marshal(BuildBoardView)", err)
	return raw
}

func TestPackBoardToolViewHasBoard(t *testing.T) {
	snap := fixtureSnapshot()
	snap.PackContentHash = PackContentHash(snap, time.Now().UTC())
	raw := marshalBoardView(t, &snap, api.BoardDetailLevelCompact, time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC))
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	board, ok := env["board"].(string)
	if !ok || board == "" {
		t.Fatalf("board field missing: %v", env)
	}
	if env["now_line"] == "" {
		t.Fatal("now_line missing")
	}
}
