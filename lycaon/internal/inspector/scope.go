package inspector

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"strings"
)

// EvidenceScope optionally narrows evidence reads to one leg or workspace.
type EvidenceScope struct {
	LegID         string
	WorkspaceID   string
	WorkspaceRoot string
}

// IsZero reports whether no scope filters apply.
func (s EvidenceScope) IsZero() bool {
	return strings.TrimSpace(s.LegID) == "" &&
		strings.TrimSpace(s.WorkspaceID) == "" &&
		strings.TrimSpace(s.WorkspaceRoot) == ""
}

// ScopeArtifactKeys are stored on evidence.Record.Artifacts for proof binding.
const (
	ArtifactLegID         = "leg_id"
	ArtifactWorkspaceID   = "workspace_id"
	ArtifactWorkspaceRoot = "workspace_root"
)

// LatestScoped returns the latest record matching scope filters.
func LatestScoped(records []evidence.Record, scope EvidenceScope) *evidence.Record {
	if len(records) == 0 {
		return nil
	}
	if scope.IsZero() {
		return Latest(records)
	}
	filtered := make([]evidence.Record, 0, len(records))
	for _, rec := range records {
		if recordMatchesScope(rec, scope) {
			filtered = append(filtered, rec)
		}
	}
	return Latest(filtered)
}

func recordMatchesScope(rec evidence.Record, scope EvidenceScope) bool {
	if id := strings.TrimSpace(scope.LegID); id != "" {
		got, _ := rec.Artifacts[ArtifactLegID].(string)
		if strings.TrimSpace(got) != id {
			return false
		}
	}
	if id := strings.TrimSpace(scope.WorkspaceID); id != "" {
		got, _ := rec.Artifacts[ArtifactWorkspaceID].(string)
		if strings.TrimSpace(got) != id {
			return false
		}
	}
	if root := strings.TrimSpace(scope.WorkspaceRoot); root != "" {
		got, _ := rec.Artifacts[ArtifactWorkspaceRoot].(string)
		if strings.TrimSpace(got) != root {
			return false
		}
	}
	return true
}

// EvidenceProjectDir selects the filesystem root for evidence JSONL reads.
func EvidenceProjectDir(primaryDir string, scope EvidenceScope) string {
	if root := strings.TrimSpace(scope.WorkspaceRoot); root != "" {
		return root
	}
	return primaryDir
}
