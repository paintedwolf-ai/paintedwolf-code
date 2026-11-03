// Package checkpoint implements durable, immutable worker checkpoints,
// the 6-step publication transaction under the checkpoint barrier, exact-base
// validation, and transactional three-way promotion (#1663).
package checkpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// FormatVersion is the durable checkpoint manifest schema version.
const FormatVersion = 1

var (
	ErrCheckpointNotFound            = errors.New("checkpoint: checkpoint not found")
	ErrInvalidManifest               = errors.New("checkpoint: invalid or unsupported manifest")
	ErrMissingBaseBlob               = errors.New("checkpoint: missing exact base blob")
	ErrCorruptBlob                   = errors.New("checkpoint: blob digest or size mismatch")
	ErrOverlayFileLimitExceeded      = errors.New("checkpoint: overlay file count limit exceeded")
	ErrOverlayByteLimitExceeded      = errors.New("checkpoint: overlay byte limit exceeded")
	ErrFlushFailed                   = errors.New("checkpoint: client dirty data or mapping flush failed")
	ErrRetentionFailed               = errors.New("checkpoint: retention reference commit failed")
	ErrOwnershipMismatch             = errors.New("checkpoint: checkpoint worker/task/project ownership mismatch")
	ErrGenerationMismatch            = errors.New("checkpoint: source generation mismatch")
	ErrStalePreviewTargetChanged     = errors.New("checkpoint: integration target changed since preview")
	ErrStalePreviewCheckpointChanged = errors.New("checkpoint: worker published a newer checkpoint since preview")
	ErrUnresolvedConflict            = errors.New("checkpoint: unresolved promotion conflict")
	ErrCheckpointRefConflict         = errors.New("checkpoint: destination git ref conflict")
	ErrUnauthorizedGitDir            = errors.New("checkpoint: gitdir or commondir outside authorized roots")
)

// TriggerKind identifies the boundary that requested checkpoint publication.
type TriggerKind string

const (
	TriggerForegroundCommand   TriggerKind = "foreground_command"
	TriggerNativeMutationBatch TriggerKind = "native_mutation_batch"
	TriggerWorkerCompletion    TriggerKind = "worker_completion"
	TriggerExplicitCapture     TriggerKind = "explicit_capture"
)

// TriggerEvent records an individual checkpoint request coalesced into a publication.
type TriggerEvent struct {
	Kind        TriggerKind `json:"kind"`
	CommandID   string      `json:"command_id,omitempty"`
	BatchID     string      `json:"batch_id,omitempty"`
	RequestedAt time.Time   `json:"requested_at"`
}

// SourceObservation records a digest/version observation on a live source path
// without claiming untouched trunk paths are frozen in the checkpoint.
type SourceObservation struct {
	Path       layeredfs.QualifiedPath `json:"path"`
	Layer      layeredfs.LayerSource   `json:"layer"`
	Kind       layeredfs.EntryKind     `json:"kind"`
	Size       int64                   `json:"size,omitempty"`
	ModTime    time.Time               `json:"mod_time,omitempty"`
	SHA256     string                  `json:"sha256,omitempty"`
	ObservedAt time.Time               `json:"observed_at"`
}

// PrivateGitSnapshot records private Git refs, index digest, and retained objects (#1663, #1671).
type PrivateGitSnapshot struct {
	HeadRef             string            `json:"head_ref,omitempty"`
	Refs                map[string]string `json:"refs,omitempty"`
	IndexDigest         string            `json:"index_digest,omitempty"`
	RetainedObjects     []string          `json:"retained_objects,omitempty"`
	RetainedObjectBytes int64             `json:"retained_object_bytes,omitempty"`
}

// ResourceAccounting tracks explicit overlay file, content, metadata, and retained object budgets.
type ResourceAccounting struct {
	ChangedFiles        int   `json:"changed_files"`
	ChangedBytes        int64 `json:"changed_bytes"`
	BaseBytes           int64 `json:"base_bytes"`
	MetadataBytes       int64 `json:"metadata_bytes"`
	RetainedObjectBytes int64 `json:"retained_object_bytes"`
	TotalOverlayBytes   int64 `json:"total_overlay_bytes"`
}

// ManifestEntry captures a single changed path, its immutable final state, and its exact base.
type ManifestEntry struct {
	Path              layeredfs.QualifiedPath   `json:"path"`
	Kind              layeredfs.EntryKind       `json:"kind"`
	Deleted           bool                      `json:"deleted,omitempty"`
	DirectoryReplaced bool                      `json:"directory_replaced,omitempty"`
	ContentChanged    bool                      `json:"content_changed,omitempty"`
	MetadataChanged   bool                      `json:"metadata_changed,omitempty"`
	Final             layeredfs.ChangeFinalAttr `json:"final,omitempty"`
	Base              layeredfs.BaseCapture     `json:"base"`
	CandidateBases    []layeredfs.BaseCapture   `json:"candidate_bases,omitempty"`
}

// Manifest is the versioned, immutable representation of a published worker checkpoint.
type Manifest struct {
	CheckpointID       string               `json:"checkpoint_id"`
	FormatVersion      int                  `json:"format_version"`
	WorkerID           string               `json:"worker_id"`
	TaskID             string               `json:"task_id"`
	ProjectID          string               `json:"project_id"`
	SourceGeneration   string               `json:"source_generation"`
	SelectedRoots      []layeredfs.RootSpec `json:"selected_roots"`
	ParentCheckpoints  []string             `json:"parent_checkpoints,omitempty"`
	Trigger            TriggerKind          `json:"trigger"`
	CoalescedTriggers  []TriggerEvent       `json:"coalesced_triggers,omitempty"`
	Entries            []ManifestEntry      `json:"entries"`
	SourceObservations []SourceObservation  `json:"source_observations,omitempty"`
	PrivateGit         PrivateGitSnapshot   `json:"private_git,omitempty"`
	ResourceAccounting ResourceAccounting   `json:"resource_accounting"`
	CreatedAt          time.Time            `json:"created_at"`
}

// CheckpointParentLayer mounts an immutable published checkpoint as a read-only
// layeredfs.ParentLayer for stacked worker views (#1663, #1666).
type CheckpointParentLayer struct {
	*layeredfs.MemoryParentLayer
	manifest Manifest
}

var _ layeredfs.ParentLayer = (*CheckpointParentLayer)(nil)

// NewCheckpointParentLayer constructs a read-only ParentLayer backed by immutable store blobs.
func NewCheckpointParentLayer(store *Store, manifest Manifest) (*CheckpointParentLayer, error) {
	if store == nil {
		return nil, fmt.Errorf("checkpoint: store is required")
	}
	mem := layeredfs.NewMemoryParentLayer(manifest.CheckpointID)
	for _, entry := range manifest.Entries {
		pe, err := manifestEntryToParentEntry(store, entry)
		if err != nil {
			return nil, err
		}
		mem.SetEntry(entry.Path, pe)
		if !entry.Deleted && entry.Kind != layeredfs.EntryAbsent {
			ensureParentDirs(mem, entry.Path, manifest.CreatedAt)
		}
	}
	return &CheckpointParentLayer{
		MemoryParentLayer: mem,
		manifest:          manifest,
	}, nil
}

func ensureParentDirs(mem *layeredfs.MemoryParentLayer, qpath layeredfs.QualifiedPath, modTime time.Time) {
	dir := path.Dir(strings.TrimSpace(qpath.Rel))
	for dir != "." && dir != "/" && dir != "" {
		anc := layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: dir}
		if _, ok, _ := mem.Lookup(anc); !ok {
			mem.SetEntry(anc, layeredfs.ParentEntry{
				Kind:    layeredfs.EntryDirectory,
				Mode:    fs.ModeDir | 0o755,
				ModTime: modTime,
			})
		}
		dir = path.Dir(dir)
	}
}

// Manifest returns the checkpoint manifest backing this parent layer.
func (l *CheckpointParentLayer) Manifest() Manifest {
	return l.manifest
}

func manifestEntryToParentEntry(store *Store, entry ManifestEntry) (layeredfs.ParentEntry, error) {
	if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
		return layeredfs.ParentEntry{Kind: layeredfs.EntryAbsent}, nil
	}
	pe := layeredfs.ParentEntry{
		Kind:       entry.Kind,
		Mode:       entry.Final.Mode,
		Size:       entry.Final.Size,
		ModTime:    entry.Final.ModTime,
		SHA256:     entry.Final.SHA256,
		LinkTarget: entry.Final.LinkTarget,
		Xattrs:     cloneXattrs(entry.Final.Xattrs),
		LinkGroup:  entry.Final.HardLinkGroup,
	}
	if entry.Kind == layeredfs.EntryRegular {
		blobPath, err := store.VerifyBlob(entry.Final.SHA256, entry.Final.Size)
		if err != nil {
			return layeredfs.ParentEntry{}, err
		}
		pe.BackingAbs = blobPath
	}
	return pe, nil
}

func cloneXattrs(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
