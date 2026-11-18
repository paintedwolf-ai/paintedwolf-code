package claim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// RootMappingRecord persists one selected root mapping inside a worker view.
type RootMappingRecord struct {
	RootID        string `json:"root_id"`
	Label         string `json:"label"`
	SourcePath    string `json:"source_path"`
	Subroot       string `json:"subroot,omitempty"`
	WorkspacePath string `json:"workspace_path"`
	IsPrimary     bool   `json:"is_primary"`
}

// AdapterMetadataFacts persists the adapter identity, preparation kind, selection and seed cost.
type AdapterMetadataFacts struct {
	AdapterID        adapter.AdapterID        `json:"adapter_id"`
	PreparationKind  adapter.PreparationKind  `json:"preparation_kind"`
	SourceVisibility adapter.SourceVisibility `json:"source_visibility"`
	SelectionReason  adapter.SelectionReason  `json:"selection_reason,omitempty"`
	DeclaredCost     adapter.MaterializeCost  `json:"declared_cost,omitzero"`
	CopiedEntries    int64                    `json:"copied_entries"`
	CopiedBytes      int64                    `json:"copied_bytes"`
	PendingGitInit   bool                     `json:"pending_git_init"`
	SeededGitRepos   []string                 `json:"seeded_git_repos,omitempty"`
}

// CheckpointMetadataRefs persists parent, latest published, and pending/failed checkpoint references.
type CheckpointMetadataRefs struct {
	ParentCheckpointIDs       []string           `json:"parent_checkpoint_ids,omitempty"`
	LastPublishedCheckpointID string             `json:"last_published_checkpoint_id,omitempty"`
	PendingCheckpointID       string             `json:"pending_checkpoint_id,omitempty"`
	FailedCheckpointReason    adapter.ReasonCode `json:"failed_checkpoint_reason,omitempty"`
}

// ViewMetadataV2 is the Format-v2 persisted branch/view metadata record.
// It records durable identity and reconstructible state, never claiming a live mount survived restart.
type ViewMetadataV2 struct {
	FormatVersion  int                    `json:"format_version"`
	Identity       ClaimIdentity          `json:"identity"`
	SelectedRoots  []RootMappingRecord    `json:"selected_roots"`
	FocusPathHints []string               `json:"focus_path_hints,omitempty"`
	Adapter        AdapterMetadataFacts   `json:"adapter"`
	JournalPath    string                 `json:"journal_path"`
	Checkpoints    CheckpointMetadataRefs `json:"checkpoints"`
	CommittedAt    time.Time              `json:"committed_at"`
}

// SaveViewMetadata validates and atomically writes Format-v2 metadata to path.
func SaveViewMetadata(path string, meta ViewMetadataV2) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: metadata path is required", ErrInvalidMetadata)
	}
	meta.FormatVersion = FormatVersionV2
	if meta.CommittedAt.IsZero() {
		meta.CommittedAt = time.Now().UTC()
	}
	if err := validateViewMetadataV2(meta); err != nil {
		return err
	}
	return writeJSONAtomically(path, meta)
}

func validateViewMetadataV2(m ViewMetadataV2) error {
	if m.FormatVersion != FormatVersionV2 {
		return fmt.Errorf("%w: expected version 2, got %d", ErrUnsupportedMetadataVersion, m.FormatVersion)
	}
	if strings.TrimSpace(m.Identity.WorkerID) == "" || strings.TrimSpace(m.Identity.ViewID) == "" {
		return fmt.Errorf("%w: worker_id and view_id are required", ErrInvalidMetadata)
	}
	if len(m.SelectedRoots) == 0 {
		return fmt.Errorf("%w: at least one selected root mapping is required", ErrInvalidMetadata)
	}
	for _, r := range m.SelectedRoots {
		if strings.TrimSpace(r.RootID) == "" || strings.TrimSpace(r.SourcePath) == "" || strings.TrimSpace(r.WorkspacePath) == "" {
			return fmt.Errorf("%w: selected root %q has empty source or workspace path", ErrInvalidMetadata, r.RootID)
		}
	}
	if strings.TrimSpace(string(m.Adapter.AdapterID)) == "" || strings.TrimSpace(m.JournalPath) == "" {
		return fmt.Errorf("%w: adapter_id and journal_path are required", ErrInvalidMetadata)
	}
	return nil
}

func writeJSONAtomically(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
