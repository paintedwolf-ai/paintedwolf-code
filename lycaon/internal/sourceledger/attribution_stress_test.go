//go:build stress

package sourceledger

import (
	"reflect"
	"strings"
	"testing"
)

func TestStressAttributionAtSourceFileLimit(t *testing.T) {
	const lines = (4 * 1024 * 1024) / 3
	before := strings.Repeat("x\r\n", lines)
	after := "b" + before[1:]
	got := ApplyAttribution([]Interval{{Start: 1, End: lines, ChangeID: "old"}}, before, after, "new")
	want := []Interval{{Start: 1, End: 1, ChangeID: "new"}, {Start: 2, End: lines, ChangeID: "old"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("large-file attribution=%+v", got)
	}
}
