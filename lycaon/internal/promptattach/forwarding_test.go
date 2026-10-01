package promptattach_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestForwardedAttachmentFromPartUsesStamp(t *testing.T) {
	attachment, ok := promptattach.ForwardedAttachmentFromPart(api.MessageContentPart{
		Origin:    api.MessageOriginAttachment,
		Source:    "recording.json",
		MediaType: "application/json",
		Path:      "prompt-attachments/deadbeef/recording.json",
		SizeBytes: 33309898,
		Content:   "preview body that must not become metadata",
	})
	if !ok {
		t.Fatal("expected forwarded attachment")
	}
	if attachment.Path != "prompt-attachments/deadbeef/recording.json" || attachment.SizeBytes != 33309898 {
		t.Fatalf("attachment = %+v", attachment)
	}
	if attachment.Kind != promptattach.ForwardedPayload {
		t.Fatalf("kind = %q", attachment.Kind)
	}
	if !strings.Contains(attachment.Hint(), `jq(path="prompt-attachments/deadbeef/recording.json")`) {
		t.Fatalf("hint = %q", attachment.Hint())
	}
}

func TestForwardedAttachmentRequiresStampedPath(t *testing.T) {
	fence := promptattach.FormatFence("notes.json", "application/json", "BODY-MUST-NOT-LEAK", false,
		`bytes="4"`, `path="prompt-attachments/aa/notes.json"`)
	_, ok := promptattach.ForwardedAttachmentFromPart(api.MessageContentPart{
		Origin:  api.MessageOriginAttachment,
		Source:  "notes.json",
		Content: fence,
	})
	if ok {
		t.Fatal("unstamped path was parsed from rendered content")
	}
}

func TestForwardedAttachmentRequiresTypedPayloadIdentity(t *testing.T) {
	for _, part := range []api.MessageContentPart{
		{Origin: api.MessageOriginAttachment, MediaType: "text/plain", Path: "prompt-attachments/aa/notes.txt", SizeBytes: 4},
		{Origin: api.MessageOriginAttachment, Source: "notes.txt", Path: "prompt-attachments/aa/notes.txt", SizeBytes: 4},
		{Origin: api.MessageOriginAttachment, Source: "notes.txt", MediaType: "text/plain", SizeBytes: 4},
		{Origin: api.MessageOriginAttachment, Source: "notes.txt", MediaType: "text/plain", Path: "prompt-attachments/aa/notes.txt"},
	} {
		if _, ok := promptattach.ForwardedAttachmentFromPart(part); ok {
			t.Fatalf("incomplete part was forwarded: %+v", part)
		}
	}
}

func TestForwardedAttachmentPathFileUsesStampedPath(t *testing.T) {
	attachment, ok := promptattach.ForwardedAttachmentFromPart(api.MessageContentPart{
		Origin:        api.MessageOriginRetrieval,
		ReferenceKind: api.MessageReferenceKindPathFile,
		Source:        "src/main.go",
		Path:          "src/main.go",
		StartLine:     12,
		EndLine:       34,
	})
	if !ok {
		t.Fatal("expected path-file attachment")
	}
	if attachment.Path != "src/main.go" || attachment.Kind != promptattach.ForwardedPathFile {
		t.Fatalf("attachment = %+v", attachment)
	}
	if attachment.Hint() != "[User attached file: src/main.go:12-34]" {
		t.Fatalf("hint = %q", attachment.Hint())
	}
}

func TestForwardedAttachmentSkipsSearchHit(t *testing.T) {
	if _, ok := promptattach.ForwardedAttachmentFromPart(api.MessageContentPart{
		Origin:        api.MessageOriginRetrieval,
		ReferenceKind: api.MessageReferenceKindSearchHit,
		Source:        "msg-9",
		Content:       "snippet",
	}); ok {
		t.Fatal("search-hit must not become an openable attachment")
	}
}
