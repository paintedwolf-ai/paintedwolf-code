package sourcebrief

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

func TestCitableBriefGivesEachRowItsOwnRecordInRenderOrder(t *testing.T) {
	brief := inject.SourceChangeBrief{
		Git:        []inject.SourceGitLine{{Kind: "commit", ToRef: "main", ToCommit: "abc12345", Detail: "subject from the repository"}},
		Files:      []inject.SourceChangeFile{{Path: "config.txt", Actor: "external", Op: "write"}, {Path: "data/ledger.txt", Actor: "user", Op: "write"}},
		OtherFiles: 4,
	}
	cited, records := citableBrief(brief, 7)

	want := []string{"source_change#7", "source_change#8", "source_change#9"}
	if got := []string{cited.Git[0].Handle, cited.Files[0].Handle, cited.Files[1].Handle}; !slices.Equal(got, want) {
		t.Fatalf("row handles = %v, want %v", got, want)
	}
	var minted []string
	for _, rec := range records {
		minted = append(minted, rec.Handle)
	}
	if !slices.Equal(minted, want) {
		t.Fatalf("record handles = %v, want %v", minted, want)
	}
	if brief.Files[0].Handle != "" || brief.Git[0].Handle != "" {
		t.Fatal("citableBrief changed its input's rows")
	}
	if cited.OtherFiles != 4 || cited.Git[0].Detail == "" || cited.Files[1].Path != "data/ledger.txt" {
		t.Fatalf("cited lost a row's facts: %+v", cited)
	}
}

func TestCitableBriefWithNoRowsMintsNothing(t *testing.T) {
	cited, records := citableBrief(inject.SourceChangeBrief{OtherFiles: 2, Truncated: true}, 1)
	if len(records) != 0 || cited.OtherFiles != 2 || !cited.Truncated {
		t.Fatalf("cited = %+v records = %+v", cited, records)
	}
}
