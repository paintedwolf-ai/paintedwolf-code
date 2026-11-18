package store

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

// EvidenceRepository persists evidence publication and its trust and custody state.
type EvidenceRepository interface {
	CommitDeliveredSource(ctx context.Context, sessionID string, observation evidence.DeliveredSourceObservation) (string, error)
	CommitEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content string) (string, string, error)
	CommitProgramHTTPProvenance(ctx context.Context, sessionID, projectDir string, prov evidence.ProgramHTTPProvenance) (string, error)
	CommitProgramObservation(ctx context.Context, sessionID, tool string, receipt *api.CommandProgramReceipt, content string) (string, error)
	CommitVisualEvidenceToolResult(ctx context.Context, sessionID, projectDir, toolName string, args map[string]any, content, artifactID string) (string, string, error)
	ExternalCustodyURLsForPaths(ctx context.Context, sessionID, projectDir string, paths []string) ([]string, error)
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
	MarkSecretExposureOnRead(ctx context.Context, sessionID, relPath string) error
	RecordContact(ctx context.Context, sessionID, host string, contact evidence.Contact) error
	SeedSecretExposure(ctx context.Context, sessionID string) error
	SeedUntrustedContent(ctx context.Context, sessionID string) error
	SessionContacts(sessionID string) map[string]evidence.Contact
	SessionSecretExposure(ctx context.Context, sessionID string) (bool, error)
	SessionURLStanding(sessionID, rawURL string) evidence.URLStanding
	SessionUntrustedContent(sessionID string) bool
	SessionUntrustedContentResult(ctx context.Context, sessionID string) (bool, error)
	UpsertEvidenceRecord(ctx context.Context, sessionID string, rec evidence.Record) error
}
