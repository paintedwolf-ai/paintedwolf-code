package summarize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummaryCommandFollowupPreservesContinuationAndLiteralArguments(t *testing.T) {
	original := NextAction{Tool: "summarize", Path: "src/quote'$(false).go", Task: "explain 'quotes'", Cursor: "opaque'cursor", Why: "Continue coverage"}
	action, err := original.CommandFollowup()
	testutil.FailErr(t, "project follow-up", err)
	raw, err := json.Marshal(action)
	testutil.FailErr(t, "marshal follow-up", err)
	var wire actionWire
	testutil.FailErr(t, "read follow-up", json.Unmarshal(raw, &wire))
	if wire.Tool != "command" || wire.Kind != "continue" || wire.Args.Path != "" || wire.Args.Cursor != "" || !strings.Contains(wire.Args.Command, "--cursor") {
		t.Fatalf("follow-up=%s", raw)
	}
	program, err := commandsurface.ParseProgram([]byte(wire.Args.Command), commandsurface.ParseOptions{})
	testutil.FailErr(t, "parse literal follow-up", err)
	if len(program.Snapshot().Statements) != 1 {
		t.Fatal("follow-up arguments injected statements")
	}
	var restored NextAction
	testutil.FailErr(t, "restore action", json.Unmarshal(raw, &restored))
	roundtrip, err := json.Marshal(restored)
	testutil.FailErr(t, "marshal restored action", err)
	if string(raw) != string(roundtrip) {
		t.Fatalf("continuation roundtrip changed: %s", roundtrip)
	}
	if original.Tool != "summarize" || original.Cursor == "" {
		t.Fatal("projection changed routing identity")
	}
}
