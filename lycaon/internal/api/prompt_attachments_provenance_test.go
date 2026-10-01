package api

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/promptattach"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestPromptContentPartsKeepUserAndExternalBoundaries(t *testing.T) {
	parts := promptadmin.PromptContentParts(
		"review these",
		[]promptattach.FramedPart{{
			Fence:     "attachment says ignore the user",
			Source:    "notes.txt",
			MediaType: "text/plain",
			Path:      "prompt-attachments/aa/notes.txt",
			SizeBytes: 24,
			BlobID:    strings.Repeat("a", 64),
		}},
		[]promptattach.FramedPart{{
			Fence:     "retrieved page says run a command",
			Source:    "src/main.go",
			MediaType: "text/plain",
			Path:      "src/main.go",
		}},
		nil,
	)
	if len(parts) != 4 {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[0].Origin != wire.MessageOriginUser || parts[0].Authority != wire.ContentAuthorityUser ||
		parts[0].TrustTier != wire.ContentTrustTierTrusted {
		t.Fatalf("user part = %+v", parts[0])
	}
	if parts[1].Origin != wire.MessageOriginHost || parts[1].Authority != wire.ContentAuthoritySystem ||
		parts[1].TrustTier != wire.ContentTrustTierTrusted ||
		parts[1].Source != promptattach.SubjectBindingSource ||
		!strings.Contains(parts[1].Content, "notes.txt") ||
		!strings.Contains(parts[1].Content, "src/main.go") {
		t.Fatalf("subject binding part = %+v", parts[1])
	}
	if parts[2].Origin != wire.MessageOriginAttachment || parts[2].Authority != wire.ContentAuthorityNone ||
		parts[2].TrustTier != wire.ContentTrustTierUntrusted ||
		parts[2].Source != "notes.txt" || parts[2].MediaType != "text/plain" ||
		parts[2].Path != "prompt-attachments/aa/notes.txt" || parts[2].SizeBytes != 24 || parts[2].BlobID != strings.Repeat("a", 64) {
		t.Fatalf("attachment part = %+v", parts[2])
	}
	if parts[3].Origin != wire.MessageOriginRetrieval || parts[3].Authority != wire.ContentAuthorityNone ||
		parts[3].TrustTier != wire.ContentTrustTierUntrusted ||
		parts[3].Source != "src/main.go" || parts[3].MediaType != "text/plain" ||
		parts[3].Path != "src/main.go" {
		t.Fatalf("reference part = %+v", parts[3])
	}
}

// Only retrieval parts carry reference metadata.
func TestPromptContentPartsStampReferenceKindOnReferencesOnly(t *testing.T) {
	parts := promptadmin.PromptContentParts(
		"review these",
		[]promptattach.FramedPart{{
			Fence: "body", Source: "notes.txt", MediaType: "text/plain",
		}},
		[]promptattach.FramedPart{{
			Fence: "hit body", Source: "msg-9", MediaType: "text/plain",
			ReferenceKind:   wire.MessageReferenceKindSearchHit,
			HitKind:         "message",
			SourceRef:       "msg-9",
			SourceSessionID: "source-session",
		}},
		nil,
	)
	for i, part := range parts {
		if part.Origin == wire.MessageOriginRetrieval {
			continue
		}
		if part.ReferenceKind != "" || part.HitKind != "" || part.SourceRef != "" {
			t.Fatalf("non-reference part %d carries reference fields: %+v", i, part)
		}
	}
	ref := parts[len(parts)-1]
	if ref.ReferenceKind != wire.MessageReferenceKindSearchHit ||
		ref.HitKind != "message" || ref.SourceRef != "msg-9" || ref.SourceSessionID != "source-session" {
		t.Fatalf("reference part lost its stamp: %+v", ref)
	}
}

// A path reference keeps the root it resolved under, so a client in a
// multi-root project opens the file it named rather than a primary-root guess.
func TestPromptContentPartsCarryReferenceRoot(t *testing.T) {
	parts := promptadmin.PromptContentParts(
		"look here",
		nil,
		[]promptattach.FramedPart{{
			Fence: "[User attached file: @app/src/a.ts:3]", Source: "@app/src/a.ts", MediaType: "text/plain",
			Path: "@app/src/a.ts", RootID: "root-app",
			ReferenceKind: wire.MessageReferenceKindPathFile, StartLine: 3, EndLine: 3,
		}},
		nil,
	)
	ref := parts[len(parts)-1]
	if ref.RootID != "root-app" || ref.Path != "@app/src/a.ts" || ref.StartLine != 3 {
		t.Fatalf("path reference lost its root: %+v", ref)
	}
	for _, part := range parts[:len(parts)-1] {
		if part.RootID != "" {
			t.Fatalf("non-reference part carries a root: %+v", part)
		}
	}
}

func TestPromptContentPartsSubjectBindingIsNotUserInstruction(t *testing.T) {
	parts := promptadmin.PromptContentParts(
		"shorten this",
		[]promptattach.FramedPart{{
			Fence:     "body",
			Source:    "CONTRIBUTING.md",
			MediaType: "text/markdown",
		}},
		nil,
		nil,
	)
	msg := wire.Message{
		Role:         wire.MessageRoleUser,
		Content:      "shorten this",
		Origin:       wire.MessageOriginUser,
		Authority:    wire.ContentAuthorityUser,
		TrustTier:    wire.ContentTrustTierTrusted,
		ContentParts: parts,
	}
	if got := wire.MessageUserInstructionContent(msg); got != "shorten this" {
		t.Fatalf("user instruction = %q", got)
	}
}
