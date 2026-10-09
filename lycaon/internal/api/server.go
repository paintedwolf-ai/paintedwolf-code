package api

import (
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/lycaon/lycaon/internal/api/capabilityadmin"
	"github.com/lycaon/lycaon/internal/api/extensionadmin"
	"github.com/lycaon/lycaon/internal/api/gitadmin"
	"github.com/lycaon/lycaon/internal/api/historyadmin"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/mcpadmin"
	"github.com/lycaon/lycaon/internal/api/modeladmin"
	"github.com/lycaon/lycaon/internal/api/projectadmin"
	"github.com/lycaon/lycaon/internal/api/promptadmin"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/researchadmin"
	"github.com/lycaon/lycaon/internal/api/scanadmin"
	"github.com/lycaon/lycaon/internal/api/searchadmin"
	"github.com/lycaon/lycaon/internal/api/sessionadmin"
	"github.com/lycaon/lycaon/internal/api/sessionview"
	"github.com/lycaon/lycaon/internal/api/settingsadmin"
	"github.com/lycaon/lycaon/internal/api/sourceapi"
	"github.com/lycaon/lycaon/internal/api/taskgroup"
	"github.com/lycaon/lycaon/internal/api/workflowadmin"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type administrativeHandlers struct {
	Extensions    extensionadmin.Handler
	Scan          scanadmin.Handler
	Settings      settingsadmin.Handler
	Capabilities  capabilityadmin.Handler
	Prompt        promptadmin.Handler
	Project       projectadmin.Handler
	Workflow      workflowadmin.Handler
	SessionAdmin  sessionadmin.Handler
	SessionView   sessionview.Projector
	Git           gitadmin.Handler
	mcpAdmin      *mcpadmin.Handler
	researchAdmin *researchadmin.Handler
	modelAdmin    *modeladmin.Handler
	historyAdmin  *historyadmin.Handler
}

type routeHandlers struct {
	Activity           *Activity
	Artifacts          *Artifacts
	Conversation       *Conversation
	Findings           *Findings
	HarnessControl     *HarnessControl
	HarnessPreparation *HarnessPreparation
	HarnessProviders   *HarnessProviders
	LocalData          *LocalData
	Storage            *Storage
	Workers            *Workers
}

type Server struct {
	Admin  administrativeHandlers
	Routes routeHandlers

	Sources        sourceapi.Handler
	router         chi.Router
	background     taskgroup.Group
	responses      httpio.Responder
	apiToken       string
	rateLimits     *rateLimitState
	recovery       *recoveryState
	restorePending atomic.Bool
	Search         searchadmin.Handler
	sessionStore   session.Store
	personActions  *personactions.Store
	sessions       *session.Manager
	fileBriefings  *filebriefing.Service
	events         events.ReplayHub
	eventPublisher *events.Publisher
	presence       *events.Presence
	harness        harnessServices
	storagePaths
	health
}

// storagePaths names host-managed durable storage.
type storagePaths struct {
	dataDir          string
	storePath        string
	workerBranchRoot string
	workerSeedRoot   string
}

// health is the boot metadata GET /health reports.
type health struct {
	storeRevision      uint64
	previousAppVersion string
	minDenVersion      string
}

func (s *Server) setupMiddleware() {
	s.router.Use(middleware.RequestID)
	s.router.Use(observability.PerformanceHTTPMiddleware())
	s.router.Use(observability.HTTPDebugMiddleware(observability.HTTPBodyCapturePolicy{
		CredentialRequest: requestBodyCarriesCredential,
		WithholdResponse:  responseBodyMustBeWithheld,
	}))
	// Suppressed in tests; targets interactive terminals.
	if os.Getenv("LYCAON_TEST") != "1" {
		s.router.Use(requestLogger())
	}
	s.router.Use(cors.Handler(corsOptions()))
	s.router.Use(s.recoverHTTPPanics)
}

// rejectEmptyPathSegments rejects ambiguous route parameters.
func (s *Server) rejectEmptyPathSegments(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "//") {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "url path contains empty segment")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// markUserPresence treats mutating requests as user activity.
func (s *Server) markUserPresence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			s.presence.MarkUserAction()
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) rejectRestorePending(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.restorePending.Load() {
			s.responses.Fail(w, wire.ApiErrorCodeBackupRestorePending,
				"restore is staged; restart before making another request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) markRestorePending() {
	s.restorePending.Store(true)
}

type healthResponse struct {
	Status                    string `json:"status"`
	Version                   string `json:"version"`
	StoreRevision             uint64 `json:"store_revision"`
	SchemaVersion             int    `json:"schema_version"`
	MinDenVersion             string `json:"min_den_version,omitempty"`
	PreviousAppVersion        string `json:"previous_app_version,omitempty"`
	StoreSchemaVersion        *int   `json:"store_schema_version,omitempty"`
	RecoveryReason            string `json:"recovery_reason,omitempty"`
	RecoveryDetail            string `json:"recovery_detail,omitempty"`
	RecoverySnapshotAvailable bool   `json:"recovery_snapshot_available"`
	RecoverySnapshotAt        string `json:"recovery_snapshot_at,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, s.healthPayload())
}

func (s *Server) healthPayload() healthResponse {
	resp := healthResponse{
		Status:             "ok",
		Version:            version.Version,
		StoreRevision:      s.storeRevision,
		SchemaVersion:      db.SchemaVersion,
		MinDenVersion:      s.minDenVersion,
		PreviousAppVersion: s.previousAppVersion,
	}
	if st := s.recovery; st != nil {
		resp.Status = "recovery"
		storeVer := st.StoreSchemaVersion
		resp.StoreSchemaVersion = &storeVer
		resp.RecoveryReason = string(st.Reason)
		resp.RecoveryDetail = st.Detail
		resp.RecoverySnapshotAvailable = st.SnapshotAvailable
		resp.RecoverySnapshotAt = st.SnapshotAt
	}
	return resp
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, ok := requestscope.Session(s.sessionStore, &s.responses, w, r, id)
	if !ok {
		return
	}
	s.Admin.SessionView.HydrateSessionWorkspace(r.Context(), sess)
	s.Admin.SessionView.EnrichSession(r.Context(), sess)
	httpio.WriteJSON(w, http.StatusOK, sess)
}
