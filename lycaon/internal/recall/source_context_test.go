package recall_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecallPreservesOriginalSourceWorkspace(t *testing.T) {
	f := newFixture(t)
	target := api.NavigationTarget{ProjectID: projectID, RootID: "original-root", WorkerID: "original-worker", Path: ".ignored/main.go", EntryKind: api.NavigationEntryKindFile}
	context := &api.SourceContext{Locations: []api.NavigationTarget{target, {ProjectID: projectID, RootID: "other-root", Path: "other/main.go", EntryKind: api.NavigationEntryKindFile}}}
	testutil.FailErr(t, "append original source message", sessionstore.NewSQL(f.db).AppendMessages(t.Context(), workerSession, api.Message{ID: "source-message", Role: api.MessageRoleTool, Content: ".ignored/main.go and other/main.go", SourceContext: context, CreatedAt: time.Now()}))
	row := evidenceRow("source-hit", workerSession, "read#9", target.Path, "navigation source lookup")
	row.MessageID = "source-message"
	f.seed(t, row)
	result := f.answer(t, coordinator(), "navigation source lookup", recall.WidenDefault)
	if len(result.Hits) != 1 || result.Hits[0].SourceContext == nil {
		t.Fatalf("missing provenance: %+v", result.Hits)
	}
	got := result.Hits[0].SourceContext
	if len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("recall changed scope or imported undisclosed paths: %+v", got)
	}
	raw, err := json.Marshal(result.Hits[0])
	testutil.FailErr(t, "encode model-facing recall hit", err)
	var wire map[string]json.RawMessage
	testutil.FailErr(t, "decode model-facing recall hit", json.Unmarshal(raw, &wire))
	if _, exists := wire["source_context"]; exists {
		t.Fatal("host navigation metadata consumed model context")
	}
}

func TestRecallCitationUsesOriginalRecordSourceContext(t *testing.T) {
	for _, observedSession := range []string{coordinatorSession, workerSession} {
		t.Run(observedSession, func(t *testing.T) {
			f := newFixture(t)
			original := api.NavigationTarget{ProjectID: projectID, RootID: "original-root", WorkerID: "original-worker", Path: "original.go", EntryKind: api.NavigationEntryKindFile}
			later := original
			later.RootID = "later-root"
			later.WorkerID = "later-worker"
			store := sessionstore.NewSQL(f.db)
			testutil.FailErr(t, "append original and citing contexts", store.AppendMessages(t.Context(), observedSession,
				api.Message{ID: "original-observation", Role: api.MessageRoleTool, Content: "original.go observed", EvidenceHandles: []string{"read#1"}, SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{original}}, CreatedAt: time.Now()},
				api.Message{ID: "later-citation", Role: api.MessageRoleAssistant, Content: "original.go cited", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{later}}, CreatedAt: time.Now()}))
			source := evidenceRow("raw-observation", observedSession, "read#1", "original.go", "original observation")
			source.HitKind = search.HitKindTool
			source.Shape = "raw"
			source.MessageID = "original-observation"
			citation := evidenceRow("citation-row", observedSession, "read#1", "original.go", "citation lookup")
			citation.Source = search.SourceMessage
			citation.MessageID = "later-citation"
			f.seed(t, source, citation)
			result := f.answer(t, coordinator(), "citation lookup", recall.WidenDefault)
			if len(result.Hits) != 1 || result.Hits[0].SourceContext == nil {
				t.Fatalf("missing canonical source context: %+v", result.Hits)
			}
			if (result.Hits[0].Record != nil) != (observedSession == coordinatorSession) {
				t.Fatal("canonical recovery widened beyond the caller session")
			}
			locations := result.Hits[0].SourceContext.Locations
			if len(locations) != 1 || locations[0] != original {
				t.Fatalf("later citation replaced original workspace: %+v", locations)
			}
		})
	}
}
