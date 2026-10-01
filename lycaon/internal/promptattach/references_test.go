package promptattach_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// testCaps loads the bundled intake policy, so tests measure what ships rather
// than numbers written twice.
func testCaps(t *testing.T) promptattach.Caps {
	t.Helper()
	caps, err := promptattach.LoadCaps()
	testutil.FailErr(t, "load bundled attachment caps", err)
	return caps
}

func ingestReferences(t *testing.T, deps promptattach.ReferenceDeps, refs []api.PromptReferencePart) (promptattach.ReferenceResult, error) {
	t.Helper()
	caps := testCaps(t)
	return promptattach.IngestReferences(deps, promptattach.NewTurnPreviewBudget(caps), refs)
}

func TestIngestReferences_pathFileFence(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(projectID, rootID, path string) (string, api.NavigationTarget, error) {
			if projectID != "proj" || rootID != "root-1" || path != "src/main.go" {
				t.Fatalf("ResolvePath(%q,%q,%q)", projectID, rootID, path)
			}
			return "src/main.go", api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: "src/main.go"}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "src/main.go",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	if len(res.Fences()) != 1 || len(res.ArtifactIDs) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Fences()[0], "[User attached file: src/main.go]") {
		t.Fatalf("fence missing read-hint:\n%s", res.Fences()[0])
	}
	if res.Parts[0].Source != "src/main.go" || res.Parts[0].MediaType != "text/plain" ||
		res.Parts[0].Path != "src/main.go" || res.Parts[0].RootID != "root-1" {
		t.Fatalf("part identity = %+v", res.Parts[0])
	}
	context := res.SourceContext()
	if len(context.Locations) != 1 || context.Locations[0].Path != "src/main.go" || context.Locations[0].RootID != "root-1" {
		t.Fatalf("attached source context = %+v", context)
	}
}

func TestIngestReferencesSourceContextKeepsCanonicalRootAndWorker(t *testing.T) {
	target := api.NavigationTarget{ProjectID: "proj", RootID: "secondary", Path: ".ignored/a.go", EntryKind: api.NavigationEntryKindFile}
	worker := target
	worker.WorkerID = "worker-job"
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, _ string) (string, api.NavigationTarget, error) {
			return "@secondary/.ignored/a.go", target, nil
		},
		LookupEvidence: func(_, _, _, _ string) (promptattach.ReferenceEvidence, error) {
			return promptattach.ReferenceEvidence{Snippet: ".ignored/a.go", HitKind: "message", SessionID: "worker-session", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{worker}}}, nil
		},
	}, []api.PromptReferencePart{
		api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{ProjectID: "proj", Path: "@secondary/.ignored/a.go"}),
		api.NewPromptReferenceSearchHit(api.PromptReferenceSearchHitPart{ProjectID: "proj", SourceRef: "message"}),
	})
	testutil.FailErr(t, "ingest addressed references", err)
	context := res.SourceContext()
	if len(context.Locations) != 2 || context.Locations[0] != target || context.Locations[1] != worker {
		t.Fatalf("reference identities were changed: %+v", context)
	}
}

func TestIngestReferences_pathFileRangeFence(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "src/main.go",
		StartLine: 12,
		EndLine:   34,
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	if !strings.Contains(res.Fences()[0], "[User attached file: src/main.go:12-34]") {
		t.Fatalf("fence missing ranged read-hint:\n%s", res.Fences()[0])
	}
}

func TestIngestReferences_pathFileRangeCarriesTheSelectedLines(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
		ReadLines: func(projectID, rootID, path string, start, end int) (string, error) {
			if projectID != "proj" || rootID != "root-1" || path != "src/main.go" || start != 12 || end != 13 {
				t.Fatalf("ReadLines(%q,%q,%q,%d,%d)", projectID, rootID, path, start, end)
			}
			return "func main() {\n\trun()", nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "src/main.go",
		StartLine: 12,
		EndLine:   13,
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	fence := res.Fences()[0]
	if !strings.Contains(fence, "[User attached file: src/main.go:12-13]\nfunc main() {\n\trun()") {
		t.Fatalf("fence missing the selected lines:\n%s", fence)
	}
	if res.Parts[0].StartLine != 12 || res.Parts[0].EndLine != 13 || res.Parts[0].Path != "src/main.go" {
		t.Fatalf("part bounds = %+v", res.Parts[0])
	}
}

func TestIngestReferences_pathFileRangeKeepsPointerWhenLinesUnavailable(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
		ReadLines: func(string, string, string, int, int) (string, error) {
			return "", errors.New("binary")
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "img.bin",
		StartLine: 3,
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	fence := res.Fences()[0]
	if !strings.Contains(fence, "[User attached file: img.bin:3]") || strings.Contains(fence, "bytes=") {
		t.Fatalf("pointer fence changed shape:\n%s", fence)
	}
}

func TestIngestReferences_wholeFileNeverReadsLines(t *testing.T) {
	_, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
		ReadLines: func(string, string, string, int, int) (string, error) {
			t.Fatal("a whole-file reference asked for lines")
			return "", nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "src/main.go",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
}

func TestSliceLines(t *testing.T) {
	const text = "one\ntwo\nthree\n"
	cases := []struct {
		name       string
		start, end int
		want       string
		wantErr    bool
	}{
		{name: "single line", start: 2, end: 2, want: "two"},
		{name: "range", start: 1, end: 2, want: "one\ntwo"},
		{name: "end clamps to last line", start: 2, end: 9, want: "two\nthree"},
		{name: "end before start collapses", start: 3, end: 1, want: "three"},
		{name: "start past end", start: 4, end: 4, wantErr: true},
		{name: "start before first", start: 0, end: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := promptattach.SliceLines(text, tc.start, tc.end)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SliceLines(%d,%d) = %q, want error", tc.start, tc.end, got)
				}
				return
			}
			testutil.FailErr(t, "slice", err)
			if got != tc.want {
				t.Fatalf("SliceLines(%d,%d) = %q, want %q", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

func TestIngestReferences_pathFolderFence(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFolder(api.PromptReferencePathFolderPart{
		ProjectID: "proj",
		RootID:    "root-1",
		Path:      "pkg",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	if !strings.Contains(res.Fences()[0], "[User attached folder: pkg]") {
		t.Fatalf("fence missing walk-hint:\n%s", res.Fences()[0])
	}
	if res.Parts[0].Path != "pkg" {
		t.Fatalf("folder path = %q", res.Parts[0].Path)
	}
}

func TestIngestReferences_pathOutOfJail(t *testing.T) {
	_, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, _ string) (string, api.NavigationTarget, error) {
			return "", api.NavigationTarget{ProjectID: "proj", RootID: "", Path: ""}, errors.New("path escapes project root boundary")
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj",
		Path:      "../secret",
	})})
	if attacherr.CodeOf(err) != attacherr.CodeOutOfJail {
		t.Fatalf("err=%v code=%q want REFERENCE_OUT_OF_JAIL", err, attacherr.CodeOf(err))
	}
}

func TestIngestReferences_artifactRelink(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		LookupArtifact: func(artifactID string) (string, error) {
			if artifactID != "art-1" {
				t.Fatalf("artifactID=%q", artifactID)
			}
			return "art-1", nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferenceArtifact(api.PromptReferenceArtifactPart{
		ProjectID:  "proj",
		ArtifactID: "art-1",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	if len(res.ArtifactIDs) != 1 || res.ArtifactIDs[0] != "art-1" {
		t.Fatalf("artifactIDs=%v", res.ArtifactIDs)
	}
	if len(res.Fences()) != 0 {
		t.Fatalf("unexpected fences=%v", res.Fences())
	}
}

func TestIngestReferences_artifactMiss(t *testing.T) {
	_, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		LookupArtifact: func(string) (string, error) {
			return "", errors.New("missing")
		},
	}, []api.PromptReferencePart{api.NewPromptReferenceArtifact(api.PromptReferenceArtifactPart{
		ProjectID:  "proj",
		ArtifactID: "missing",
	})})
	if attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("err=%v code=%q want UNSUPPORTED_ATTACHMENT", err, attacherr.CodeOf(err))
	}
}

func TestIngestReferences_searchHitUsesStoredSnippet(t *testing.T) {
	res, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		LookupEvidence: func(projectID, sourceRef, hitKind, sessionID string) (promptattach.ReferenceEvidence, error) {
			if projectID != "proj" || sourceRef != "msg-9" || hitKind != "message" {
				t.Fatalf("LookupEvidence(%q,%q,%q,%q)", projectID, sourceRef, hitKind, sessionID)
			}
			return promptattach.ReferenceEvidence{Snippet: "stored snippet body", HitKind: "message", SessionID: "source-session"}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferenceSearchHit(api.PromptReferenceSearchHitPart{
		ProjectID: "proj",
		SourceRef: "msg-9",
		HitKind:   "message",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
	fence := res.Fences()[0]
	if !strings.Contains(fence, "[User attached search result: message msg-9]") {
		t.Fatalf("missing search hint:\n%s", fence)
	}
	if !strings.Contains(fence, "stored snippet body") {
		t.Fatalf("missing stored snippet:\n%s", fence)
	}
	if got := res.Parts[0].SourceSessionID; got != "source-session" {
		t.Fatalf("SourceSessionID = %q want source-session", got)
	}
}

func TestIngestReferences_folderNeverCallsArtifactOrEvidence(t *testing.T) {
	_, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
		LookupArtifact: func(string) (string, error) {
			t.Fatal("LookupArtifact must not run for path-folder")
			return "", nil
		},
		LookupEvidence: func(string, string, string, string) (promptattach.ReferenceEvidence, error) {
			t.Fatal("LookupEvidence must not run for path-folder")
			return promptattach.ReferenceEvidence{Snippet: "", HitKind: "", SessionID: ""}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFolder(api.PromptReferencePathFolderPart{
		ProjectID: "proj",
		Path:      "docs",
	})})
	if err != nil {
		t.Fatalf("IngestReferences: %v", err)
	}
}

func TestIngestReferences_projectMismatch(t *testing.T) {
	_, err := ingestReferences(t, promptattach.ReferenceDeps{
		SessionProjectID: "proj-a",
		ResolvePath: func(_, _, path string) (string, api.NavigationTarget, error) {
			return path, api.NavigationTarget{ProjectID: "proj", RootID: "root-1", Path: path}, nil
		},
	}, []api.PromptReferencePart{api.NewPromptReferencePathFile(api.PromptReferencePathFilePart{
		ProjectID: "proj-b",
		Path:      "a.go",
	})})
	if attacherr.CodeOf(err) != attacherr.CodeOutOfJail {
		t.Fatalf("err=%v code=%q want REFERENCE_OUT_OF_JAIL", err, attacherr.CodeOf(err))
	}
}
