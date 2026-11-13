package filebriefing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Summaries are never shown while they are written, so a generation that
// streams publishes only its settled states.
func TestGenerationPublishesNoPartialText(t *testing.T) {
	service, _, _ := serviceFixture(t, generationFunc(func(_ context.Context, _ GenerationRequest, emit func(string)) (string, error) {
		emit("- Purpose: Builds")
		emit(" the binary")
		return recordedSummary, nil
	}))
	hub := events.NewMemoryHub()
	service.events = hub
	target := serviceTarget("main.go")
	target.ProjectID = uuid.NewString()
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: target.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()
	_, err = service.Request(t.Context(), target, "manual")
	testutil.FailErr(t, "request", err)
	for _, want := range []string{"pending", "complete"} {
		select {
		case envelope := <-stream:
			var event wire.FileBriefingEvent
			testutil.FailErr(t, "decode", json.Unmarshal(envelope.Data, &event))
			if event.Status != want {
				t.Fatalf("published %q; want %q", event.Status, want)
			}
		case <-t.Context().Done():
			t.Fatalf("no %s event", want)
		}
	}
}

func TestFileBriefingPreviewPersistsFallbackText(t *testing.T) {
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	briefings := NewMemory()
	server := NewService(t.Context(), Dependencies{Store: briefings, Config: cfg})
	briefing := Briefing{
		ProjectID: "project-1", RootID: "root-1", Path: "main.go", TargetKey: "target-1",
		Presentation: "current", SourceSHA256: "sha-1", Trigger: "automatic",
		Status: StatusPending, UpdatedAt: time.Now().UTC(),
	}
	briefing, err = briefings.Start(t.Context(), briefing)
	testutil.FailErr(t, "start file briefing", err)
	server.previewFileBriefing(t.Context(), briefing, "Bounded fallback text.", true)

	got, err := briefings.Get(t.Context(), briefing.ProjectID, briefing.RootID, briefing.Path, briefing.TargetKey)
	testutil.FailErr(t, "get file briefing", err)
	if got.FallbackText != "Bounded fallback text." || !got.Truncated {
		t.Fatalf("preview = %+v", got)
	}
}

func TestFileBriefingLocationsDTOCarriesHostDeclarationIdentity(t *testing.T) {
	locations := fileBriefingLocationsDTO([]Location{{Line: 12, Name: "Serve", Kind: "function"}})
	if len(locations) != 1 || locations[0].Line != 12 || locations[0].Name != "Serve" || locations[0].Kind != "function" {
		t.Fatalf("locations = %+v", locations)
	}
}

func TestFileBriefingJobKeySeparatesGenerationAttempts(t *testing.T) {
	briefing := Briefing{
		ProjectID: "project-1", RootID: "root-1", Path: "main.go", TargetKey: "target-1", AttemptID: "attempt-1",
	}
	first := fileBriefingJobKey(briefing)
	briefing.AttemptID = "attempt-2"
	if second := fileBriefingJobKey(briefing); first == second {
		t.Fatal("generation attempts share a job key")
	}
}

func TestCompletedFileBriefingPartialEndsAtLastUsefulSentence(t *testing.T) {
	value := "Purpose explains the package clearly. Flow was still being"
	if got := completedPartial(value, 20); got != "Purpose explains the package clearly." {
		t.Fatalf("completedPartial = %q", got)
	}
	if got := completedPartial("Too short.", 20); got != "" {
		t.Fatalf("short partial = %q", got)
	}
}
