package sourcebrief

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCitableBriefGivesEachRowItsOwnRecordInRenderOrder(t *testing.T) {
	brief := inject.SourceChangeBrief{
		Git:        []inject.SourceGitLine{{Kind: "commit", ToRef: "main", ToCommit: "abc12345", Detail: "subject from the repository"}},
		Files:      []inject.SourceChangeFile{{Path: "config.txt", Actor: "external", Op: "write"}, {Path: "data/ledger.txt", Actor: "user", Op: "write"}},
		OtherFiles: 4,
	}
	cited, records, err := citableBrief(brief, 7)
	testutil.FailErr(t, "citableBrief", err)

	got := []string{cited.Git[0].Handle, cited.Files[0].Handle, cited.Files[1].Handle}
	want := []string{"source_change#7", "source_change#8", "source_change#9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("row handles = %v, want %v", got, want)
	}
	if len(records) != len(want) {
		t.Fatalf("records = %+v", records)
	}
	for i, rec := range records {
		if rec.Handle != want[i] {
			t.Fatalf("record %d handle = %q, want %q", i, rec.Handle, want[i])
		}
	}
	if brief.Files[0].Handle != "" || brief.Git[0].Handle != "" {
		t.Fatal("citableBrief changed its input's rows")
	}
	if cited.OtherFiles != 4 {
		t.Fatalf("counted files = %d", cited.OtherFiles)
	}

	var gitFact inject.SourceGitLine
	testutil.FailErr(t, "decode git fact", json.Unmarshal([]byte(records[0].Body[0]), &gitFact))
	if gitFact.ToCommit != "abc12345" || gitFact.Detail != "" {
		t.Fatalf("git fact = %+v, want the commit and no subject", gitFact)
	}
	if cited.Git[0].Detail == "" {
		t.Fatal("the brief row lost its subject")
	}
	var fileFact inject.SourceChangeFile
	testutil.FailErr(t, "decode file fact", json.Unmarshal([]byte(records[1].Body[0]), &fileFact))
	if fileFact.Path != "config.txt" || fileFact.Actor != "external" || fileFact.Op != "write" {
		t.Fatalf("file fact = %+v", fileFact)
	}
}

func TestCitableBriefWithNoRowsMintsNothing(t *testing.T) {
	cited, records, err := citableBrief(inject.SourceChangeBrief{OtherFiles: 2, Truncated: true}, 1)
	testutil.FailErr(t, "citableBrief", err)
	if len(records) != 0 || cited.OtherFiles != 2 || !cited.Truncated {
		t.Fatalf("cited = %+v records = %+v", cited, records)
	}
}
