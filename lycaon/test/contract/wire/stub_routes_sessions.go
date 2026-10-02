package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/hostidentity"
	"github.com/lycaon/lycaon/pkg/api"
)

func registerStubWorkspaceRoutes(mux *http.ServeMux, now time.Time, writeJSON stubJSONWriter) {
	registerStubSessionSupportRoutes(mux, writeJSON)
	registerStubProjectRoutes(mux, now, writeJSON)
	registerStubProjectSourceRoutes(mux, writeJSON)
	registerStubSourceViewRoutes(mux, writeJSON)
	registerStubSourceHistoryRoutes(mux, writeJSON)
	registerStubSourceGitReviewRoutes(mux, writeJSON)
	registerStubEditorRoutes(mux, writeJSON)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":                      "ok",
			"version":                     "0.1.0",
			"store_revision":              1,
			"schema_version":              1,
			"recovery_snapshot_available": false,
		})
	})

	mux.HandleFunc("GET /v1/host", func(w http.ResponseWriter, r *http.Request) {
		public := make([]byte, 32)
		identity := hostidentity.Identity{HostID: hostidentity.DeriveHostID(public), PublicKey: public}
		writeJSON(w, http.StatusOK, api.HostInfo{
			HostID: identity.HostID, HostPublicKey: identity.EncodedPublicKey(),
			ProductVersion: "1.0.0", ContractVersion: "1.0.0",
			Caller:       api.Person{ID: fixtureSessionID, Role: api.PersonRoleOwner},
			Capabilities: []api.HostCapability{api.HostCapabilitySharedDevice},
		})
	})

	mux.HandleFunc("GET /v1/diagnostics/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	})

	mux.HandleFunc("GET /v1/preflight", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.PreflightReport{
			Overall: "ok",
			Probes: []api.PreflightProbe{
				{ID: "os_version", Status: "ok"},
			},
			AttachmentCapabilities: api.AttachmentCapabilities{
				AutoAttachPasteBytes:     1 << 14,
				MaxInlineTextBytes:       1 << 16,
				MaxAttachments:           10,
				MaxReferences:            10,
				MaxImages:                4,
				MaxUploadBytes:           1 << 20,
				MaxImageBytes:            1 << 20,
				MaxBodyBytes:             1 << 21,
				MaxTurnBytes:             1 << 22,
				MaxBodyPreviewBytes:      1 << 14,
				MaxLargeTextPreviewBytes: 1 << 13,
				MaxTurnPreviewBytes:      1 << 15,
				MaxDocumentBytes:         1 << 20,
				MaxVideoBytes:            1 << 20,
				VideoMIMETypes:           []string{"video/mp4", "video/webm"},
				ImageMIMETypes:           []string{"image/png"},
				TextMIMETypes:            []string{"text/plain"},
				TextExtensions:           []string{".md"},
				TextBasenames:            []string{"README"},
			},
		})
	})

	session := api.Session{
		ID:            fixtureSessionID,
		WorkspacePath: fixtureProjectDir,
		Posture:       api.SessionPostureSpec,
		Status:        api.SessionStatusIdle,
		CreatedAt:     now,
		ActivityAt:    now,
		UpdatedAt:     now,
	}

	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, session)
	})
	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, session)
	})
	mux.HandleFunc("PATCH /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req api.UpdateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: err.Error()})
			return
		}
		out := session
		if req.Title != nil {
			out.Title = strings.TrimSpace(*req.Title)
		}
		if req.Archived != nil && *req.Archived {
			out.ArchivedAt = &now
		}
		if req.Pinned != nil && *req.Pinned {
			rank := 1
			out.PinRank = &rank
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("DELETE /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/projects/{id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionListPage{
			Sessions: []api.SessionSummary{{
				ID:           fixtureSessionID,
				ProjectID:    fixtureProjectID,
				Title:        "fixture chat",
				Posture:      api.SessionPostureSpec,
				Status:       api.SessionStatusIdle,
				MessageCount: 2,
				CreatedAt:    now,
				ActivityAt:   now,
			}},
			Total: 1,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionTranscriptPage{Messages: []api.Message{}, TurnClocks: map[string]api.TurnClock{}, TurnLoads: map[string][]api.TurnLoad{}})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/invocations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.InvocationReceiptList{Invocations: []api.InvocationReceipt{}})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionBootstrap{
			Session:           session,
			Transcript:        api.SessionTranscriptPage{Messages: []api.Message{}, TurnClocks: map[string]api.TurnClock{}, TurnLoads: map[string][]api.TurnLoad{}},
			Progress:          api.ProgressDigest{Steps: []api.ProgressStep{}},
			Findings:          api.FindingsDigest{Findings: []api.Finding{}},
			Queue:             api.QueueDraft{QueueItems: []api.QueueItem{}},
			Workers:           []api.WorkerTask{},
			Checkpoints:       []api.CheckpointEvent{},
			BackgroundOutputs: []api.BackgroundProcessOutput{},
			Previews:          []api.PreviewAttachment{},
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="session-deadbeef.json"`)
		writeJSON(w, http.StatusOK, api.SessionTranscriptPage{Messages: []api.Message{}, TurnClocks: map[string]api.TurnClock{}, TurnLoads: map[string][]api.TurnLoad{}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/prompts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, api.PromptAcceptedResponse{
			Status: "queued", OperationID: "00000000-0000-4000-8000-000000000001", MessageID: "00000000-0000-4000-8000-000000000001",
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		idle := session
		idle.Status = api.SessionStatusIdle
		writeJSON(w, http.StatusOK, idle)
	})
	mux.HandleFunc("POST /v1/sessions/{id}/rewind/preview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.RewindPreviewResponse{
			PlanDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Files:      []api.RewindPreviewFile{{RootID: fixtureProjectID, Path: "src/foo.go", TargetPath: "src/foo.go"}},
			Issues:     []api.RewindIssue{}, TruncatedMessageCount: 2,
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/rewind", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.RewindSessionResponse{
			RestoredPaths:         []string{"src/foo.go"},
			TruncatedMessageCount: 2,
			RestoredPrompt:        "the ask that was rewound",
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
		read := session
		seenAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		read.SeenAt = &seenAt
		writeJSON(w, http.StatusOK, read)
	})
}

func registerStubSessionSupportRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	mux.HandleFunc("GET /v1/sessions/{id}/messages/{message_id}/content", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ChatContentPage{
			Reference: api.ChatContentReference{Field: "tool_output", SHA256: strings.Repeat("a", 64), Rows: 1},
			Complete:  true,
			Spans:     []api.RedactedSpan{},
			Rows:      []api.ChatContentRow{{Spans: []api.RedactedSpan{}}},
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/messages/{message_id}/content/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ChatContentSearchPage{Matches: []api.ChatContentMatch{}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/composer-secrets", func(w http.ResponseWriter, r *http.Request) {
		secret := stubManagedSecret()
		secret.Scope = "chat"
		secret.Origin = "composer_marked"
		secret.Format = ""
		secret.EntropyBits = 0
		writeJSON(w, http.StatusCreated, secret)
	})
	mux.HandleFunc("GET /v1/sessions/{id}/background", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackgroundProcessListResponse{Processes: []api.BackgroundProcess{}})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/background/{process_id}/output", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackgroundProcessOutput{
			ProcessID: r.PathValue("process_id"), Chunks: []api.BackgroundProcessChunk{},
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/background/{process_id}/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.BackgroundProcessStopResult{ProcessID: r.PathValue("process_id"), StopRequested: true, Running: false})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(": ping\n\n"))
	})
	mux.HandleFunc("POST /v1/sessions/{id}/compact", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionCompactResponse{
			Generation:      1,
			TokensBefore:    120000,
			TokensAfter:     80000,
			ChunksCompacted: 2,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionContextResponse{
			EstimatedTokens:      95000,
			BudgetRemaining:      0,
			CompactionGeneration: 1,
			OversizedChunks:      1,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/findings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.FindingsDigest{
			Findings: []api.Finding{},
			Revision: 0,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/drafts/{slot_id}/versions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.DraftVersionsResponse{Versions: []api.DraftVersion{}})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ProgressDigest{
			Steps:    []api.ProgressStep{},
			Revision: 0,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ArtifactListResponse{Artifacts: []api.ArtifactListItem{}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/artifacts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.VisualArtifact{
			ID:     "live-tool-recording-fixture",
			Mime:   "video/mp4",
			Source: api.VisualArtifactSourceCapture,
			PageID: "page-fixture",
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/artifacts/{artifact_id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.QueueDraft{
			QueueItems: []api.QueueItem{},
			Hold:       false,
			Revision:   0,
		})
	})
	mux.HandleFunc("PATCH /v1/sessions/{id}/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.QueueDraft{
			QueueItems: []api.QueueItem{},
			Hold:       false,
			Revision:   0,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/coordinator-context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CoordinatorRunContext{
			WorkflowID:       "hotfix-session",
			WorkflowVersion:  "1.0.0",
			CurrentPhase:     "stub",
			RunStatus:        string(api.WorkflowRunStatusRunning),
			CoordinatorBrief: "Extends plan@1.0.0. Skips parent phases: research.",
			HasComposeDraft:  true,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CheckpointListResponse{Checkpoints: []api.CheckpointEvent{}})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/checkpoints/{checkpoint_id}/unlock-challenges", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, api.PresenceChallenge{
			ChallengeID: fixtureSessionID, ProofPayload: "cHJlc2VuY2U", Prompt: "Unlock Deploy key for this chat in Painted Wolf Code.",
			ExpiresAt: fixtureTime,
		})
	})
	mux.HandleFunc("GET /v1/sessions/{id}/vault", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ChatVault{
			ChatSessionID: fixtureSessionID, Unlocked: true, UnlockedAt: fixtureTime, ClosesAt: fixtureTime, ExpiresAt: fixtureTime,
		})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/vault/lock", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.ChatVault{ChatSessionID: fixtureSessionID})
	})
	mux.HandleFunc("POST /v1/vault/lock", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.LockVaultResponse{Locked: 1})
	})
	mux.HandleFunc("POST /v1/sessions/{id}/checkpoints/{checkpoint_id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.CheckpointResponse{
			ID:        fixtureSessionID,
			SessionID: fixtureSessionID,
			Kind:      api.CheckpointKindToolApproval,
			Status:    api.CheckpointStatusApproved,
		})
	})

}
