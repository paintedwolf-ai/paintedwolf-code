package filebriefing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFileBriefingDeltaEmitterPreservesWhitespaceChunks(t *testing.T) {
	projectID := uuid.NewString()
	hub := events.NewMemoryHub()
	stream, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe file briefing events", err)
	defer unsubscribe()

	cfg, err := LoadConfig()
	testutil.FailErr(t, "load file briefing config", err)
	cfg.Stream.ChunkChars = 1_000
	cfg.Stream.FlushMS = 10_000
	server := NewService(t.Context(), Dependencies{Store: NewMemory(), Config: cfg, Events: hub})
	emitter := newFileBriefingDeltaEmitter(t.Context(), server, Briefing{
		ProjectID: projectID, RootID: "root-1", Path: "main.go", SourceSHA256: "sha-1",
		AttemptID: "attempt-1", UpdatedAt: time.Now().UTC(),
	})
	emitter.Add(t.Context(), "Purpose:")
	emitter.Add(t.Context(), " ")
	emitter.Add(t.Context(), "builds the app")
	emitter.Flush(t.Context())

	var combined string
	for range 2 {
		select {
		case envelope := <-stream:
			if envelope.Topic != wire.EventTopicFileBriefing {
				t.Fatalf("topic = %q", envelope.Topic)
			}
			var event wire.FileBriefingEvent
			testutil.FailErr(t, "decode file briefing event", json.Unmarshal(envelope.Data, &event))
			if event.Status != "streaming" || event.SourceSHA256 != "sha-1" || event.AttemptID != "attempt-1" {
				t.Fatalf("event = %+v", event)
			}
			combined += event.Delta
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for file briefing delta")
		}
	}
	if combined != "Purpose: builds the app" {
		t.Fatalf("combined delta = %q", combined)
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
