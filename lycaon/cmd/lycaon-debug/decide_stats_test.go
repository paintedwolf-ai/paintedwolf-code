package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// statsOutput runs decide stats against the fixture's store and decodes what it prints.
func statsOutput(t *testing.T, dbPath string) map[string]json.RawMessage {
	t.Helper()
	read, write, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	stdout := os.Stdout
	os.Stdout = write
	runErr := runDecideStats([]string{"--db", dbPath})
	os.Stdout = stdout
	testutil.FailErr(t, "close pipe", write.Close())
	testutil.FailErr(t, "decide stats", runErr)
	raw, err := io.ReadAll(read)
	testutil.FailErr(t, "read stats", err)
	var out map[string]json.RawMessage
	testutil.FailErr(t, "decode stats", json.Unmarshal(raw, &out))
	return out
}

func TestDecideStatsCountsEachLoadableToolsLoadsAndUses(t *testing.T) {
	f := newExportFixture(t)
	f.receipt(t, f.root.ID, "u1", `{"host":"coordinator"}`,
		`{"turn":{"candidates":[{"id":"web_search"},{"id":"jq_edit"},{"id":"command"}],"tools":{"jq_edit":0.9,"command":0.7}}}`)
	f.receipt(t, f.root.ID, "u2", `{"host":"coordinator"}`, `not json`)
	out := statsOutput(t, f.path)
	var turns int
	testutil.FailErr(t, "decode turns", json.Unmarshal(out["turns"], &turns))
	var tools []toolStats
	testutil.FailErr(t, "decode tools", json.Unmarshal(out["tools"], &tools))
	if turns != 1 {
		t.Fatalf("turns = %d, want the one decodable turn receipt", turns)
	}
	want := []toolStats{{ID: "command", Loaded: 1, Receipts: 1}, {ID: "jq_edit", Loaded: 1, Used: 1, Receipts: 1}, {ID: "web_search", Receipts: 1}}
	if len(tools) != len(want) {
		t.Fatalf("tools = %+v, want %+v", tools, want)
	}
	for i := range want {
		if tools[i] != want[i] {
			t.Fatalf("tools[%d] = %+v, want %+v", i, tools[i], want[i])
		}
	}
	if _, ok := out["units"]; ok {
		t.Fatal("stats still report units")
	}
}
