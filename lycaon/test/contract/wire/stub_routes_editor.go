package contract

import (
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubEditorRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	changeTS := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	editorDocument := api.EditorDocument{
		ID: fixtureCheckpointID, ProjectID: fixtureProjectID, FileID: fixtureFileID, WorkspaceID: fixtureWorkspaceID,
		RootID: fixtureBlueprintID, Path: "main.go",
		BaseSHA256: strings.Repeat("a", 64), Encoding: api.SourceEncodingUTF8, SizeBytes: 13,
		EOL: "lf", BaseEOL: "lf", Revision: 1,
		Epoch: 1, StateVector: []byte{0}, CRDTUpdate: []byte{0, 0}, PublishedRevision: 1, Participants: []api.EditorParticipant{},
		SecretScreenStatus: api.SecretScreenUnavailable,
	}
	mux.HandleFunc("GET /v1/projects/{id}/source/workspace", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceWorkspace{
			WorkspaceID: fixtureWorkspaceID,
			Roots: []api.SourceWorkspaceRoot{{
				ID: "11111111-1111-4111-8111-111111111111", Path: "/fixture/root",
				Watch: api.SourceWatchCoverage{State: api.SourceWatchLive, Recursive: true},
			}},
			Inventory: api.SourceInventoryState{Status: "ready", Complete: true},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/attachments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.AttachmentUploadResponse{
			BlobID:   strings.Repeat("a", 64),
			Filename: "notes.txt",
			Mime:     "text/plain",
			Kind:     api.AttachmentKindText,
			Bytes:    12,
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, editorDocument)
	})
	mux.HandleFunc("PUT /v1/projects/{id}/editor-documents/{document_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("PUT /v1/projects/{id}/editor-documents/retention", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EditorDocumentStatuses{Documents: []api.EditorDocumentStatus{}, Missing: []string{}})
	})
	editorReplica := api.EditorReplicaFrame{
		ID: editorDocument.ID, ProjectID: editorDocument.ProjectID, FileID: editorDocument.FileID, WorkspaceID: editorDocument.WorkspaceID,
		RootID: editorDocument.RootID, Path: editorDocument.Path, BaseSHA256: editorDocument.BaseSHA256,
		Encoding: editorDocument.Encoding, EOL: editorDocument.EOL, BaseEOL: editorDocument.BaseEOL,
		Revision: 1, Epoch: 1, StateVector: []byte{0}, CRDTUpdate: []byte{0, 0}, PublishedRevision: 1,
		Participants: []api.EditorParticipant{}, SecretScreenStatus: editorDocument.SecretScreenStatus,
	}
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/updates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorReplica)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorReplica)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/leave", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/changes/{change_id}/revert", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/presence", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /v1/projects/{id}/editor-documents/{document_id}/changes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.EditorDocumentChanges{Changes: []api.EditorDocumentChange{}})
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/discard", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/reload", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/observe", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/save", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, editorDocument)
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/secret-spans/preview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SecretMarkPreview{
			Eligible: true, Start: 10, End: 33, RuneLength: 23, ByteLength: 23,
			Shape: "aa-aaaaa-9AA9-9AA9-AA99", TrimmedLeading: 1, TrimmedTrailing: 1,
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/secret-ignores", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SecretIgnoreList{Rules: []api.SecretIgnoreRule{}})
	})
	mux.HandleFunc("POST /v1/projects/{id}/secret-ignores", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.SecretIgnoreList{Rules: []api.SecretIgnoreRule{}})
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/secret-ignores/{entry_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/projects/{id}/secret-ignore-candidates/{candidate_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SecretIgnoreCandidate{Value: "public fixture"})
	})
	mux.HandleFunc("POST /v1/projects/{id}/editor-documents/{document_id}/secret-spans/mark", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ManagedSecret{
			Reference: "{{paintedwolf-secret:cccccccc-cccc-4ccc-8ccc-cccccccccccc}}",
			Name:      "Staging relay token", Purpose: "Authenticates the staging relay",
			Scope: "project", Origin: "file_marked", CreatedAt: fixtureTime, State: "active",
		})
	})

	mux.HandleFunc("POST /v1/projects/{id}/source/seen", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/source/seen/{file_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/seen", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceSeenList{
			Files: []api.SourceSeenFile{{
				FileID: fixtureFileID, RootID: fixtureRootID, Path: "src/fixture.ts",
				Tip:    api.SourceTip{State: api.SourceTipStateContent, Sha256: "2413fb3709b05939f04cf2e92f7d0897fc2596f9ad0b8a9ea855c7bfebaae892"},
				SeenAt: changeTS, ThroughOrdinal: 1,
				Effects: []api.SourceWalkEffect{},
			}},
		})
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/pins", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourcePinList{
			Pins: []api.SourcePin{{
				ID: "33333333-3333-4333-8333-333333333333", ProjectID: fixtureProjectID,
				Label: "review point", CreatedAt: changeTS,
			}},
		})
	})
	mux.HandleFunc("POST /v1/projects/{id}/source/pins", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.SourcePin{
			ID: "33333333-3333-4333-8333-333333333333", ProjectID: fixtureProjectID,
			Label: "review point", CreatedAt: changeTS,
		})
	})
	mux.HandleFunc("PATCH /v1/projects/{id}/source/pins/{pin_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourcePin{
			ID: r.PathValue("pin_id"), ProjectID: fixtureProjectID,
			Label: "review point", CreatedAt: changeTS,
		})
	})
	mux.HandleFunc("DELETE /v1/projects/{id}/source/pins/{pin_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/projects/{id}/source/storage", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SourceStorage{
			Inventory: api.SourceInventoryState{
				Status: "uninitialized",
			},
			Storage: api.StorageUsageReport{
				Lanes: []api.StorageUsageLane{
					{Lane: "artifacts", Scope: "project", UsedBytes: 512},
					{Lane: "source_blobs", Scope: "device", UsedBytes: 1024},
				},
			},
		})
	})
	mux.HandleFunc("GET /v1/workers/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.WorkerJobChangesResponse{
			Files: []api.WorkerJobChangedFile{
				{RootID: "root-1", Path: "src/fixture.ts", Op: api.SourceChangeOpWrite},
			},
		})
	})

}
