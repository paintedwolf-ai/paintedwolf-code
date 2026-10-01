package contract

import (
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubSourceHistoryRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	historyAction := &api.SourceHistoryAction{
		ID: fixtureCheckpointID, Label: "Undo move of fixture.ts", Kind: "move",
		RootID: fixtureRootID, Path: "src/fixture.ts", FromPath: "fixture.ts",
		ToPath: "src/fixture.ts", IsDir: false, RemovesPath: true,
	}
	mux.HandleFunc("GET /v1/projects/{id}/source/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceHistoryState{Undo: historyAction})
	})
	historyMutation := api.SourceHistoryMutationResponse{
		EntryID: fixtureCheckpointID, RootID: fixtureRootID, Path: "fixture.ts",
		FromPath: "src/fixture.ts", Op: api.SourceChangeOpRename, IsDir: false,
	}
	mux.HandleFunc("POST /v1/projects/{id}/source/history/undo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, historyMutation)
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/history/redo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, historyMutation)
	})

	briefing := api.FileBriefingResponse{
		TargetKey:    "fixture-current",
		RootID:       fixtureRootID,
		Path:         "src/fixture.ts",
		Presentation: "current",
		SourceSHA256: "2413fb3709b05939f04cf2e92f7d0897fc2596f9ad0b8a9ea855c7bfebaae892",
		Status:       "complete",
		Preview: api.FileBriefingPreview{
			Language: "typescript", LineCount: 1,
		},
		Locations: []api.FileBriefingLocation{{
			Line: 1, Name: "fixture", Kind: "variable",
		}},
		Sections: []api.FileBriefingSection{{
			Kind: "purpose", Text: "Defines the `fixture` constant.",
		}},
		UpdatedAt: fixtureTime,
	}
	mux.HandleFunc("GET /v1/projects/{id}/source/file-briefings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, briefing)
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/file-briefings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, briefing)
	})

	changeTS := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	fixtureSessionID := "11111111-1111-4111-8111-111111111111"
	fixtureAfterSha := "3f79bb7b435b05321651daefd374cdc681dc06faa65e374e38337b88ca046dea"
	mux.HandleFunc("GET /v1/projects/{id}/source/walk-summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceWalkSummary{Turns: []api.SourceWalkTurnSummary{}})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/walk", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceWalkResponse{
			Turns:           []api.SourceWalkTurn{},
			CommitAvailable: true,
			Baseline:        "head",
			GitChanges:      []api.SourceGitChange{},
			Commands:        []api.SourceCommandWindow{},
			Files: []api.SourceWalkFile{{
				FileID:                fixtureFileID,
				RootID:                fixtureRootID,
				Path:                  "src/fixture.ts",
				LastAt:                &changeTS,
				ChangedSincePresented: true,
				Tip: api.SourceTip{
					State:  api.SourceTipStateContent,
					Sha256: "b2c3",
				},
				HeadMatch: api.SourceHeadMatchSame,
				Effects: []api.SourceWalkEffect{{
					Contributors:   []api.SourceContributor{},
					ID:             "22222222-2222-4222-8222-222222222222",
					ProjectID:      fixtureProjectID,
					OperationID:    "operation-fixture",
					FileID:         fixtureFileID,
					AfterVersionID: "version-fixture",
					WorkspaceKind:  api.SourceWorkspaceKindProject,
					RootID:         fixtureRootID,
					Path:           "src/fixture.ts",
					EntryKind:      "file",
					Op:             api.SourceChangeOpWrite,
					Origin:         api.SourceChangeOriginAgent,
					SessionID:      &fixtureSessionID,
					Turn:           3,
					Ordinal:        1,
					Cause:          "fixture",
					CaptureQuality: "exact",
					ObservedAt:     changeTS,
				}},
			}},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceFileVersionsResponse{
			FileID: fixtureFileID, Current: api.SourceTip{
				State: api.SourceTipStateContent, Sha256: fixtureAfterSha,
			},
			Commits:         []api.SourceFileCommit{},
			Arrivals:        []api.SourceGitChange{},
			GitHistoryState: api.SourceGitHistoryStateAvailable,
			Versions: []api.SourceFileVersion{{
				ID: "version-fixture", FileID: fixtureFileID,
				WorkspaceKind: api.SourceWorkspaceKindProject,
				OperationID:   stubPtr("operation-fixture"),
				EffectID:      stubPtr("22222222-2222-4222-8222-222222222222"),
				RootID:        fixtureRootID,
				Path:          "src/fixture.ts", State: "content", ContentSha256: fixtureAfterSha,
				SizeBytes: 28, CaptureState: "stored", CaptureQuality: "exact",
				Landing: "working_file",
				Op:      stubPtr(api.SourceChangeOpWrite),
				Origin:  stubPtr(api.SourceChangeOriginAgent),
				Cause:   stubPtr("fixture"), Ordinal: 1, CreatedAt: changeTS,
			}},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/versions/{version_id}/restore", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceVersionRestoreResponse{
			VersionID: r.PathValue("version_id"), PreviousVersionID: fixtureVersionID,
			FileID: fixtureFileID, RootID: fixtureRootID, Path: "src/fixture.ts",
			State: api.SourceTipStateContent, Sha256: fixtureAfterSha, Changed: true,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/editable", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, api.MakeProjectSourceEditableResponse{
			RootID:       fixtureRootID,
			Path:         "src/fixture.ts",
			PreviousMode: 0o444,
			Mode:         0o644,
			Writable:     true,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/commits/{commit_sha}/restore", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceCommitRestoreResponse{
			Commit: strings.Repeat("a", 40), PreviousVersionID: fixtureVersionID,
			FileID: fixtureFileID, RootID: fixtureRootID, Path: "src/fixture.ts",
			State: api.SourceTipStateContent, Sha256: fixtureAfterSha, Changed: true,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/comparison", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceComparison{
			InRange:  true,
			EffectID: "22222222-2222-4222-8222-222222222222",
			FileID:   fixtureFileID,
			Op:       api.SourceChangeOpWrite,
			Before: &api.SourceComparisonSide{
				State: "content", Availability: "available",
				Content: "export const fixture = false;\n",
			},
			After: &api.SourceComparisonSide{
				State: "content", Sha256: fixtureAfterSha, Availability: "available",
				Content: "export const fixture = true;\n",
			},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/attribution", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceAttributionResponse{
			HeadSha256: fixtureAfterSha,
			Intervals: []api.SourceAttributionInterval{{
				StartLine:  1,
				EndLine:    1,
				SessionID:  &fixtureSessionID,
				Turn:       3,
				RecordedAt: changeTS,
			}},
		})
	})
}
