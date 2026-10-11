package api

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/version"
)

func storeFailure(database db.Handle) *db.StoreIncompatibleError {
	health, ok := database.(interface{ Failure() error })
	if !ok {
		return nil
	}
	var failure *db.StoreIncompatibleError
	_ = errors.As(health.Failure(), &failure)
	return failure
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
	st := s.recovery
	if failure := storeFailure(s.database); failure != nil {
		st = &recoveryState{Reason: failure.Reason, Detail: failure.Detail, StoreSchemaVersion: failure.StoreSchemaVersion}
	}
	resp.setRecovery(st)
	return resp
}

func (resp *healthResponse) setRecovery(st *recoveryState) {
	if st != nil {
		resp.Status = "recovery"
		storeVer := st.StoreSchemaVersion
		resp.StoreSchemaVersion = &storeVer
		resp.RecoveryReason = string(st.Reason)
		resp.RecoveryDetail = st.Detail
		resp.RecoverySnapshotAvailable = st.SnapshotAvailable
		resp.RecoverySnapshotAt = st.SnapshotAt
	}
}
