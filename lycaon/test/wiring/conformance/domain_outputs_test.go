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
func verifyInspectFileOutputs(t *testing.T, observed runtimeObservation) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(observed.Outcome.Stdout))
	decode := outcomeDecoder(t, decoder, observed)
	var outline nativesurvey.ReadResponse
	decode("decode native outline", &outline)
	if outline.Path != "code.go" || outline.Mode != "outline" || outline.TotalLines != 6 || len(outline.Symbols) != 1 || outline.Symbols[0].Name != "Alpha" || outline.Symbols[0].Line != 4 {
		t.Fatalf("outline = %+v", outline)
	}
	var summary nativesurvey.SummarizeResponse
	decode("decode native summary", &summary)
	if summary.Task != "Alpha" || len(summary.Pack.Identity) == 0 || summary.Pack.Identity[0].Path != "code.go" {
		t.Fatalf("summary = %+v", summary)
	}
	requireNoFurtherOutput(t, decoder)
}

func verifyInspectTreeOutputs(t *testing.T, observed runtimeObservation) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(observed.Outcome.Stdout))
	decode := outcomeDecoder(t, decoder, observed)
	var maps [2]reposurvey.RepoResponse
	for i := range maps {
		decode("decode native repository map", &maps[i])
		if maps[i].Bundle != "layout_overview" || maps[i].Digest == "" || maps[i].Groups == 0 || maps[i].EntriesExamined == 0 {
			t.Fatalf("repository map %d = %+v", i, maps[i])
		}
	}
	if maps[0].InventoryState == "ready" && maps[1].InventoryState == "ready" && maps[0].EntriesExamined < maps[1].EntriesExamined {
		t.Fatalf("depth two examined fewer entries than depth one: %+v", maps)
	}
	requireNoFurtherOutput(t, decoder)
}

// outcomeDecoder reports the whole program outcome on a decode failure, so a
// truncated or empty stdout shows its exit status and capture state.
func outcomeDecoder(t *testing.T, decoder *json.Decoder, observed runtimeObservation) func(string, any) {
	return func(step string, v any) {
		t.Helper()
		if err := decoder.Decode(v); err != nil {
			t.Fatalf("%s: %v\noutcome: %+v", step, err, observed.Outcome)
		}
	}
}

func requireNoFurtherOutput(t *testing.T, decoder *json.Decoder) {
	t.Helper()
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output after native domain results: %v (%v)", extra, err)
	}
}

func verifyDomainToolArguments(t *testing.T, observed runtimeObservation) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(observed.Outcome.Stdout))
	var layout reposurvey.RepoResponse
	testutil.FailErr(t, "decode bundle survey", decoder.Decode(&layout))
	if layout.Bundle != "layout_overview" {
		t.Fatalf("survey = %+v", layout)
	}
	for _, source := range []string{"path with a pattern", "supplied content"} {
		var summary nativesurvey.SummarizeResponse
		testutil.FailErr(t, "decode summary of "+source, decoder.Decode(&summary))
		if summary.Task != "Alpha" {
			t.Fatalf("summary of %s = %+v", source, summary)
		}
	}
}
