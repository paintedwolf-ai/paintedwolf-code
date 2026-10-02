package contract

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubProjectRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	fixtureName := "fixture"
	fixtureProject := api.Project{
		ID:              fixtureProjectID,
		Name:            &fixtureName,
		Roots:           []api.ProjectRoot{{ID: fixtureRootID, Path: fixtureProjectDir, Label: "fixture", IsPrimary: true, AddedAt: now, Kind: "attached"}},
		RootsGeneration: 0,
		SessionCount:    0,
		LastOpenedAt:    now,
		CreatedAt:       now,
	}
	mux.HandleFunc("GET /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectListResponse{Projects: []api.Project{fixtureProject}})
	})
	mux.HandleFunc("POST /v1/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fixtureProject)
	})
	mux.HandleFunc("GET /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fixtureProject)
	})
	mux.HandleFunc("GET /v1/projects/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ArtifactListResponse{Artifacts: []api.ArtifactListItem{}})
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/artifacts/{artifact_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	secretFixture := stubManagedSecret()
	secretForPath := func(r *http.Request) api.ManagedSecret {
		out := secretFixture
		out.Reference = "{{paintedwolf-secret:" + r.PathValue("secret_id") + "}}"
		return out
	}
	mux.HandleFunc("GET /v1/projects/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ManagedSecretList{Secrets: []api.ManagedSecret{secretFixture}})
	})
	mux.HandleFunc("POST /v1/projects/{id}/secrets", func(w http.ResponseWriter, r *http.Request) {
		entered := secretFixture
		entered.Origin = "settings_entered"
		entered.Format, entered.EntropyBits = "", 0
		entered.Purpose = "Fixture purpose"
		writeJSON(w, http.StatusCreated, entered)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/secrets/{secret_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, secretForPath(r))
	})
	mux.HandleFunc("PUT /v1/projects/{id}/secrets/{secret_id}/value", func(w http.ResponseWriter, r *http.Request) {
		rotated := secretForPath(r)
		rotated.Version = 2
		rotatedAt := fixtureTime
		rotated.ValueReplacedAt = &rotatedAt
		writeJSON(w, http.StatusOK, rotated)
	})
	mux.HandleFunc("GET /v1/projects/{id}/secrets/{secret_id}/uses", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ManagedSecretUseList{
			Uses: []api.ManagedSecretUse{{
				Delivery: "handed_off", ToolCallID: "test-tool-call",
				UsedAt: fixtureTime, ToolName: "http_request", Outcome: "resolved", Version: 1,
			}},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/secrets/{secret_id}/reveal-challenges", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ManagedSecretRevealChallenge{
			ChallengeID:  fixtureCheckpointID,
			ProofPayload: "Zml4dHVyZS1wcm9vZg",
			Prompt:       "Authenticate to reveal Fixture token.",
			Version:      1,
			ExpiresAt:    fixtureTime,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/secrets/{secret_id}/reveal-challenges/{challenge_id}/complete", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ManagedSecretRevealResponse{
			SecretValue:   "fixture-credential",
			Version:       1,
			RevealedAt:    fixtureTime,
			RemaskAfterMs: 5000,
		})
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/secrets/{secret_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fixtureProject)
	})
	trustFixture := api.ProjectTrust{
		Review:    api.ProjectTrustReview{ID: fixtureCheckpointID, ProjectID: fixtureProject.ID, Changes: []api.TrustFileChange{}},
		ProjectID: fixtureProject.ID,
		Surfaces: []api.ProjectTrustSurface{{
			ID:             api.TrustSurfaceProjectSettings,
			Label:          "Approvals & limits",
			Group:          api.TrustGroupSteering,
			Count:          1,
			Items:          []api.TrustSurfaceItem{{Name: settingsoverlay.Rel("limits.yaml")}},
			DeviceEnabled:  true,
			ProjectEnabled: true,
			Applying:       true,
			Seen:           true,
		}},
	}
	mux.HandleFunc("GET /v1/projects/{id}/trust", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trustFixture)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/trust", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trustFixture)
	})
	mux.HandleFunc("POST /v1/projects/{id}/trust/review", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, trustFixture)
	})
	mux.HandleFunc("GET /v1/projects/{id}/agent-presence", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.AgentPresenceSnapshot{ProjectID: fixtureProject.ID, Sessions: []api.AgentSessionPresence{}})
	})
	mux.HandleFunc("GET /v1/projects/{id}/agent-context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectAgentContext{
			ProjectID: fixtureProject.ID, RootID: r.URL.Query().Get("root_id"), Path: ".",
			InstructionsEnabled: true, SkillsEnabled: true,
			Instructions: []api.AgentContextInstruction{}, Skills: []api.AgentContextSkill{},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/removal-assessment", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectRemovalAssessment{
			ProjectID: fixtureProjectID, AssessmentToken: "reviewed", ExtensionRevision: "device-revision", Complete: true,
			Checks: []api.ProjectRemovalCheck{}, Extensions: []api.ProjectRemovalExtension{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/removals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, api.ProjectRemovalResult{
			OperationID: fixtureCheckpointID, ProjectID: fixtureProjectID, ProjectState: "deleted",
			CleanupState: "not_requested", Extensions: []string{},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/removals/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectRemovalResult{
			OperationID: fixtureCheckpointID, ProjectID: fixtureProjectID, ProjectState: "deleted",
			CleanupState: "not_requested", Extensions: []string{},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/roots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fixtureProject)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/roots/{root_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/roots/{root_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, fixtureProject)
	})
	mux.HandleFunc("POST /v1/projects/{id}/promotion", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fixtureProject)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/promotion", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/clone", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, fixtureProject)
	})
	mux.HandleFunc("GET /v1/projects/detect", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.FolderDetect{
			Path: fixtureProjectDir,
		})
	})
}

func registerStubProjectSourceRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	mux.HandleFunc("GET /v1/projects/{id}/source/browse", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceDirListing{
			WorkspaceID: fixtureWorkspaceID,
			RootID:      fixtureRootID,
			Dir:         ".",
			Entries: []api.SourceDirEntry{
				{Name: "src", IsDir: true},
				{Name: "README.md", IsDir: false},
			},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/index", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceIndexSummary{
			State: api.SourceIndexStateReady, Revision: 1, Roots: []api.SourceIndexRootSummary{},
			Coverage: []api.SourceIndexRootCoverage{},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceSearchResponse{
			State: api.SourceIndexStateReady, Revision: 1, Matches: []api.SourceSearchMatch{},
			Coverage: []api.SourceIndexRootCoverage{},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/symbols", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceSymbolsResponse{
			Symbols: []api.SourceSymbol{
				{Name: "fixture", Kind: api.SourceSymbolKindConstant, Line: 1},
			},
			Truncated: false,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/definition", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceDefinitionResponse{
			Candidates: []api.SourceDefinitionCandidate{{
				RootID:  fixtureRootID,
				Path:    "src/fixture.ts",
				Line:    1,
				Kind:    api.SourceSymbolKindConstant,
				Snippet: "export const fixture = true;",
			}},
			Truncated: false,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/editorconfig", func(w http.ResponseWriter, r *http.Request) {
		trim := true
		writeJSON(w, http.StatusOK, api.SourceEditorConfig{
			Path: "src/fixture.ts", RootID: fixtureRootID,
			IndentStyle: "space", IndentSize: 2, TrimTrailingWhitespace: &trim,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	})
	sourceRead := api.ProjectSourceReadResponse{
		WorkspaceID:   fixtureWorkspaceID,
		WorkspaceKind: api.SourceWorkspaceKindProject,
		Path:          "src/fixture.ts",
		Content:       "export const fixture = true;\n",
		OverLimit:     false,
		Binary:        false,
		SizeBytes:     28,
		MIME:          "text/plain",
		SHA256:        "2413fb3709b05939f04cf2e92f7d0897fc2596f9ad0b8a9ea855c7bfebaae892",
	}
	mux.HandleFunc("GET /v1/projects/{id}/source", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, sourceRead)
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/operations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceOperationList{Operations: []api.SourceOperationStatus{}})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/operations/{operation_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/operations/{operation_id}/retry", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, api.SourceOperationStatus{OperationID: r.PathValue("operation_id"), State: "running", Operation: "deleteProjectSource", Phase: "preparing", Cancelable: true, CreatedAt: fixtureTimeValue()})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceOperationStatus{
			OperationID: r.PathValue("operation_id"), Complete: true, State: "completed", Operation: "deleteProjectSource", Phase: "recording",
			EntriesProcessed: 1, CreatedAt: fixtureTimeValue(),
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.ProjectSourceEntryCreatedResponse{Path: "src/new-fixture.ts"})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/rename", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectSourceLifecycleResponse{
			RootID: fixtureRootID,
			Path:   "src/renamed.ts",
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/copy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectSourceLifecycleResponse{
			RootID: fixtureRootID,
			Path:   "src/copy.ts",
		})
	})
	mux.HandleFunc("PUT /v1/projects/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProjectSourceWriteResponse{
			Path:      "src/fixture.ts",
			SizeBytes: 30,
			SHA256:    "b5c8f0a41f2a30f4ba4f4ba0f0f0aa11bb22cc33dd44ee55ff6677889900aabb",
		})
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/source", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}
