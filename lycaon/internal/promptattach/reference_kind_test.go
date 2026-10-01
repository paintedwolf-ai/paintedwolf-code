package promptattach_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/pkg/api"
)

// Reference kinds come from host metadata.
func TestIngestReferences_stampsReferenceKind(t *testing.T) {
	deps := promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
		LookupEvidence: func(_, sourceRef, hitKind, _ string) (promptattach.ReferenceEvidence, error) {
			return promptattach.ReferenceEvidence{Snippet: "stored snippet body", HitKind: hitKind, SessionID: "source-session"}, nil
		},
	}

	res, err := ingestReferences(t, deps, []api.PromptReferencePart{
		api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
			ProjectID: "proj", RootID: "root-1", Path: "src/main.go",
			StartLine: 12, EndLine: 34,
		}),
		api.NewPromptReferencePathFolder(api.PromptReferencePathFolderPart{
			ProjectID: "proj", RootID: "root-1", Path: "pkg",
		}),
		api.NewPromptReferenceSearchHit(api.PromptReferenceSearchHitPart{
			ProjectID: "proj", SourceRef: "msg-9", HitKind: "message",
		}),
	})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	if len(res.Parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(res.Parts))
	}

	file := res.Parts[0]
	if file.ReferenceKind != api.MessageReferenceKindPathFile {
		t.Errorf("path-file kind = %q", file.ReferenceKind)
	}
	// Line bounds stay structured beside the bare path.
	if file.Source != "src/main.go" || file.Path != "src/main.go" {
		t.Errorf("path-file source/path = %q/%q want the bare path", file.Source, file.Path)
	}
	if file.StartLine != 12 || file.EndLine != 34 {
		t.Errorf("path-file scope = %d-%d want 12-34", file.StartLine, file.EndLine)
	}

	folder := res.Parts[1]
	if folder.ReferenceKind != api.MessageReferenceKindPathFolder {
		t.Errorf("path-folder kind = %q", folder.ReferenceKind)
	}
	if folder.StartLine != 0 || folder.EndLine != 0 {
		t.Errorf("path-folder carries a line scope: %d-%d", folder.StartLine, folder.EndLine)
	}

	hit := res.Parts[2]
	if hit.ReferenceKind != api.MessageReferenceKindSearchHit {
		t.Errorf("search-hit kind = %q", hit.ReferenceKind)
	}
	if hit.HitKind != "message" || hit.SourceRef != "msg-9" {
		t.Errorf("search-hit identity = %q/%q want message/msg-9", hit.HitKind, hit.SourceRef)
	}
}

// A whole-file reference has no scope to report — zero, not a synthesized 1-1.
func TestIngestReferences_wholeFileHasNoLineScope(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj", RootID: "root-1", Path: "src/main.go",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	part := res.Parts[0]
	if part.StartLine != 0 || part.EndLine != 0 {
		t.Fatalf("whole-file scope = %d-%d want 0-0", part.StartLine, part.EndLine)
	}
}
