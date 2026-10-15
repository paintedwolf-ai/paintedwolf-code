package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPublishedMigrationIdentitiesSurviveImplementationMoves(t *testing.T) {
	const root = "lycaon/internal/db/migrations/"
	raw := []byte(`package migrations; func A() Step { return Step{ID:"original",Checksum:"` + strings.Repeat("a", 64) + `"} }`)
	before := treeRelease{root + "old.go": raw}
	for _, tc := range []struct {
		name   string
		source treeRelease
		want   string
	}{
		{name: "unchanged", source: before},
		{name: "moved", source: treeRelease{root + "new.go": raw}},
		{name: "removed", source: treeRelease{}, want: "removed"},
		{name: "changed", source: treeRelease{root + "old.go": []byte(strings.ReplaceAll(string(raw), strings.Repeat("a", 64), strings.Repeat("b", 64)))}, want: "checksum"},
		{name: "computed identity", source: treeRelease{root + "old.go": []byte(strings.ReplaceAll(string(raw), `ID:"original"`, `ID:name`))}, want: "literal"},
		{name: "duplicate", source: treeRelease{root + "old.go": raw, root + "new.go": raw}, want: "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMigrationHistory(before, tc.source)
			if tc.want == "" {
				testutil.FailErr(t, "preserve released migration", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("migration=%v, want %s", err, tc.want)
			}
		})
	}
}
