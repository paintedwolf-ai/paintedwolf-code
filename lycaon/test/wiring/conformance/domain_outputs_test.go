package conformance_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	nativesurvey "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// Domain commands have typed native results, rather than shell reference tools.
func verifyInspectDomainOutputs(t *testing.T, observed runtimeObservation) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(observed.Outcome.Stdout))
	var outline nativesurvey.ReadResponse
	testutil.FailErr(t, "decode native outline", decoder.Decode(&outline))
	if outline.Path != "code.go" || outline.Mode != "outline" || outline.TotalLines != 6 || len(outline.Symbols) != 1 || outline.Symbols[0].Name != "Alpha" || outline.Symbols[0].Line != 4 {
		t.Fatalf("outline = %+v", outline)
	}
	var maps [2]reposurvey.RepoResponse
	for i := range maps {
		testutil.FailErr(t, "decode native repository map", decoder.Decode(&maps[i]))
		if maps[i].Bundle != "layout_overview" || maps[i].Digest == "" || maps[i].Groups == 0 || maps[i].EntriesExamined == 0 {
			t.Fatalf("repository map %d = %+v", i, maps[i])
		}
	}
	if maps[0].InventoryState == "ready" && maps[1].InventoryState == "ready" && maps[0].EntriesExamined < maps[1].EntriesExamined {
		t.Fatalf("depth two examined fewer entries than depth one: %+v", maps)
	}
	var summary nativesurvey.SummarizeResponse
	testutil.FailErr(t, "decode native summary", decoder.Decode(&summary))
	if summary.Task != "Alpha" || len(summary.Pack.Identity) == 0 || summary.Pack.Identity[0].Path != "code.go" {
		t.Fatalf("summary = %+v", summary)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output after native domain results: %v (%v)", extra, err)
	}
}
