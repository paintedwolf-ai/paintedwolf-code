// Package obligation defines the landed-change scan handoff.
package obligation

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Plan is the scan policy decision recorded with a landed change.
type Plan struct {
	ID                   string
	ScanID               string
	AssessmentID         string
	WorkerJobID          string
	CanonicalPath        string
	DelegationID         string
	WorkflowRunID        string
	ScannerID            string
	ChangedPaths         []string
	DeletedPaths         []string
	ExecutionManifest    api.ScanExecutionManifest
	ExecutionFingerprint string
	FingerprintScheme    string
	PathScoped           bool
	Required             bool
	InitialFailure       string
	InitialFailureCode   string
	CreatedAt            time.Time
}

// Empty reports whether no durable landed-change record should be written.
func (p Plan) Empty() bool {
	return p.ID == "" && p.ScanID == "" && p.AssessmentID == "" && p.WorkerJobID == "" && p.CanonicalPath == "" &&
		p.DelegationID == "" && p.WorkflowRunID == "" && p.ScannerID == "" &&
		len(p.ChangedPaths) == 0 && len(p.DeletedPaths) == 0 && !p.PathScoped &&
		p.ExecutionManifest == (api.ScanExecutionManifest{}) && p.ExecutionFingerprint == "" && p.FingerprintScheme == "" &&
		!p.Required && p.InitialFailure == "" && p.InitialFailureCode == "" && p.CreatedAt.IsZero()
}
