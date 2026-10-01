package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistrySummarizerCurate_surveyListDirSelection(t *testing.T) {
	preview := "dir files=12 subdirs=tools entries=foo.go"
	snapshot := evidence.AssembleLedger([]evidence.Record{
		{
			Handle:     "snap#1",
			Kind:       "list",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "list_dir",
			Survey:     true,
			Path:       "lycaon/internal",
			LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
			Body:       []string{"1\t" + preview},
		},
	})
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"lycaon/internal","line":1,"excerpt":"subdirs=tools"}],"gloss":[{"label":"tools subtree"}]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Tool: "list_dir", View: "map", Target: "lycaon/internal"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(got.Selections) != 1 {
		t.Fatalf("selections = %#v", got.Selections)
	}
	if got.Report.Dropped != 0 {
		t.Fatalf("dropped = %d want 0", got.Report.Dropped)
	}
}
