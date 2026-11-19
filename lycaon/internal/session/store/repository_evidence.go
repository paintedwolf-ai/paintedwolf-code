package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// EvidenceRepository persists evidence publication and its trust and custody state.
type EvidenceRepository interface {
	BeginProgramPublication(ctx context.Context, sessionID, programID string) error
	CommitDeliveredSource(ctx context.Context, sessionID string, observation evidence.DeliveredSourceObservation) (string, error)
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error)
	CommitProgramHTTPProvenance(ctx context.Context, target evidence.ProgramProvenanceTarget, prov evidence.ProgramHTTPProvenance) (string, error)
	CommitProgramPublication(ctx context.Context, sessionID, programID string) error
	CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error)
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error)
	ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error)
	ListRecoverableProgramPublications(ctx context.Context, afterProgramID string, limit int) ([]evidence.ProgramPublicationKey, error)
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
	MarkProgramPublicationMaterialized(ctx context.Context, sessionID, programID string) error
	MarkSecretExposureOnRead(ctx context.Context, sessionID, relPath string) error
	RecordContact(ctx context.Context, sessionID, host string, contact evidence.Contact) error
	SeedSecretExposure(ctx context.Context, sessionID string) error
	SeedUntrustedContent(ctx context.Context, sessionID string) error
	SessionContacts(sessionID string) map[string]evidence.Contact
	SessionSecretExposure(ctx context.Context, sessionID string) (bool, error)
	SessionURLStanding(ctx context.Context, sessionID, rawURL string) evidence.URLStanding
	SessionUntrustedContent(sessionID string) bool
	SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error)
	StageProgramPublication(ctx context.Context, target evidence.ProgramProvenanceTarget, prov evidence.ProgramHTTPProvenance) error
	UpsertEvidenceRecord(ctx context.Context, sessionID string, rec evidence.Record) error
}
