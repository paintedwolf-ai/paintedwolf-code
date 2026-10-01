package api

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// recoveryState is the store diagnosis exposed by GET /health.
type recoveryState struct {
	Reason             db.RecoveryReason
	Detail             string
	StoreSchemaVersion int
	SnapshotAvailable  bool
	SnapshotAt         string // RFC 3339 UTC; empty when unavailable
}

// RecoveryServerOpts configures the recovery-only server.
type RecoveryServerOpts struct {
	DBPath       string
	DataDir      string
	APIToken     string
	Incompatible *db.StoreIncompatibleError
	Logger       *slog.Logger
}

// NewRecoveryServer serves recovery routes for an incompatible store.
func NewRecoveryServer(ctx context.Context, opts RecoveryServerOpts) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		responses:    httpio.Responder{Logger: logger},
		apiToken:     strings.TrimSpace(opts.APIToken),
		storagePaths: storagePaths{dataDir: strings.TrimSpace(opts.DataDir), storePath: strings.TrimSpace(opts.DBPath)},
		recovery:     buildRecoveryState(ctx, opts.DBPath, opts.Incompatible),
	}
	if backup.HasPendingRestore(opts.DataDir) {
		s.markRestorePending()
	}

	s.router = chi.NewRouter()
	s.router.Use(middleware.RequestID)
	if os.Getenv("LYCAON_TEST") != "1" {
		s.router.Use(requestLogger())
	}
	s.router.Use(cors.Handler(corsOptions()))
	s.router.Use(s.recoverHTTPPanics)

	s.rateLimits = newRateLimitState()
	registerRootOperation(s.router, operationGetHealth, s.handleHealth)
	s.router.Route("/v1", func(r chi.Router) {
		r.Use(s.requireClientAuth)
		r.Use(s.bindRecoveryCaller)
		r.Use(s.rejectRestorePending)
		r.Use(s.rejectEmptyPathSegments)
		s.registerV1Operation(r, operationGetBackupCapabilities, s.handleBackupCapabilities)
		s.registerV1Operation(r, operationRestoreBackup, s.handleRestoreBackup)
		s.registerV1Operation(r, operationRestoreRecoverySnapshot, s.handleRestoreRecoverySnapshot)
		s.registerV1Operation(r, operationResetStore, s.handleResetStore)
	})
	s.router.NotFound(s.handleStoreIncompatible)
	s.router.MethodNotAllowed(s.handleStoreIncompatible)
	return s
}

func (s *Server) bindRecoveryCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(people.WithCaller(r.Context(), people.Person{
			ID:   "recovery-operator",
			Role: wire.PersonRoleOwner,
		})))
	})
}

func buildRecoveryState(ctx context.Context, dbPath string, incompatible *db.StoreIncompatibleError) *recoveryState {
	st := &recoveryState{
		Reason: db.RecoveryReasonIntegrityFailed,
		Detail: "Your conversation history did not pass its integrity check.",
	}
	if incompatible != nil {
		st.Reason = incompatible.Reason
		st.StoreSchemaVersion = incompatible.StoreSchemaVersion
		// The detail carries the diagnostic cause beneath the client-rendered summary.
		if incompatible.Detail != "" {
			st.Detail = incompatible.Detail
		}
	}
	available, at := probeRecoverySnapshot(ctx, dbPath)
	st.SnapshotAvailable = available
	st.SnapshotAt = at
	return st
}

// probeRecoverySnapshot advertises self-contained recovery snapshots with supported upgrade routes.
func probeRecoverySnapshot(ctx context.Context, dbPath string) (available bool, at string) {
	if strings.TrimSpace(dbPath) == "" {
		return false, ""
	}
	_, record, err := backup.LatestUpgradeRecovery(ctx, filepath.Dir(dbPath))
	if err != nil {
		return false, ""
	}
	return true, record.CreatedAt
}

func (s *Server) handleStoreIncompatible(w http.ResponseWriter, r *http.Request) {
	detail := "store incompatible with this build"
	if s.recovery != nil && s.recovery.Detail != "" {
		detail = s.recovery.Detail
	}
	s.responses.Fail(w, wire.ApiErrorCodeStoreIncompatible, detail)
}
