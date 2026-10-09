package tooloutput

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPageCompactionWalksWholeInventory(t *testing.T) {
	const total = 226
	seen := map[string]bool{}
	for offset := 0; offset < total; {
		end := min(offset+50, total)
		groups := []map[string]any{}
		for i := offset; i < end; i++ {
			groups = append(groups, map[string]any{"id": fmt.Sprintf("group:%d", i), "description": strings.Repeat("evidence ", 1000)})
		}
		page := map[string]any{"page_contract": PageContract{"groups", "revision-1"}, "groups": groups, "offset": offset, "total_match": total}
		if end < total {
			page["next_offset"] = end
		}
		raw, err := json.Marshal(page)
		testutil.FailErr(t, "marshal page", err)
		spilled := WireSpillToolOutput(t.TempDir(), Screened(string(raw)), 1800, 0)
		if spilled.RejectCode != "" {
			t.Fatalf("spill rejected: %s", spilled.RejectCode)
		}
		// A second, tighter projection must retain the same contiguous contract.
		out, _ := FitWireJSON(spilled.Preview, 1200)
		var got struct {
			Groups []struct{ ID string }
			Next   *int `json:"next_offset"`
		}
		testutil.FailErr(t, "decode delivered page", json.Unmarshal([]byte(out), &got))
		if len(got.Groups) == 0 {
			t.Fatal("page made no progress")
		}
		for i, g := range got.Groups {
			if g.ID != fmt.Sprintf("group:%d", offset+i) || seen[g.ID] {
				t.Fatalf("skipped or repeated group at %d: %s", offset+i, g.ID)
			}
			seen[g.ID] = true
		}
		offset += len(got.Groups)
		if got.Next != nil && *got.Next != offset {
			t.Fatalf("cursor skipped delivered page: %d != %d", *got.Next, offset)
		}
		if got.Next == nil && offset != total {
			t.Fatal("missing continuation")
		}
	}
	if len(seen) != total {
		t.Fatalf("visited %d groups", len(seen))
	}
}
