package surface_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionUserTaskSkipsLoopWake(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build CVE GUI"},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake, Content: surface.HostLoopWakeSentinel},
	}
	if got := surface.SessionUserTask(history); got != "Build CVE GUI" {
		t.Fatalf("task = %q", got)
	}
}

func TestSessionForwardedAttachmentsSkipsInternalAndDedupes(t *testing.T) {
	history := []api.Message{
		{
			Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
			ContentParts: []api.MessageContentPart{{
				Origin: api.MessageOriginAttachment, Source: "recording.json",
				MediaType: "application/json",
				Path:      "prompt-attachments/aaa/recording.json",
				SizeBytes: 33309898,
			}},
		},
		{
			Role: api.MessageRoleUser, Origin: api.MessageOriginHost,
			Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake,
			Content: surface.HostLoopWakeSentinel,
			ContentParts: []api.MessageContentPart{{
				Origin: api.MessageOriginAttachment, Source: "ignored.json",
				Path: "prompt-attachments/zzz/ignored.json",
			}},
		},
		{
			Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
			ContentParts: []api.MessageContentPart{
				{
					Origin: api.MessageOriginAttachment, Source: "recording.json",
					Path: "prompt-attachments/aaa/recording.json", SizeBytes: 33309898,
				},
				{
					Origin: api.MessageOriginRetrieval, ReferenceKind: api.MessageReferenceKindPathFile,
					Source: "src/main.go", Path: "src/main.go",
				},
			},
		},
	}
	got := surface.SessionForwardedAttachments(history)
	if len(got) != 2 {
		t.Fatalf("handles = %+v", got)
	}
	if got[0].Path != "prompt-attachments/aaa/recording.json" || got[0].SizeBytes != 33309898 {
		t.Fatalf("payload = %+v", got[0])
	}
	if got[1].Path != "src/main.go" || got[1].Kind != "path_file" {
		t.Fatalf("path-file = %+v", got[1])
	}
}

func TestSessionUserTaskSkipsPostSpawnCoordinatorRejection(t *testing.T) {
	reject := "Rejected: This worker profile requires write mode.\nCode: TASK_SCOPE_WRITE_REQUIRED"
	history := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Refactor game.py into a clean multi-file architecture"},
		{Role: api.MessageRoleAssistant, Content: "dispatching workers"},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindCoordinatorGuidance, HostSignalID: "TASK_SCOPE_WRITE_REQUIRED", Content: reject},
	}
	if got := surface.SessionUserTask(history); got != "Refactor game.py into a clean multi-file architecture" {
		t.Fatalf("task = %q, want original user ask not rejection nudge", got)
	}
}
