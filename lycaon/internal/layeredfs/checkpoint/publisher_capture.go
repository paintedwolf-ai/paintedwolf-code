package checkpoint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (p *Publisher) captureAndStageManifest(
	req PublishRequest,
	batch []*pendingPublish,
	inject func(FaultStage) error,
) (Manifest, []string, error) {
	enumerated, err := p.cfg.Core.EnumerateChanges(true)
	if err != nil {
		return Manifest{}, nil, err
	}
	// A repository a worker created in its view is private nodes in Core. .git is never promoted as files.
	changes := make([]layeredfs.ChangeEntry, 0, len(enumerated))
	for _, ce := range enumerated {
		if !layeredfs.IsGitMetadataPath(ce.Path.Rel) {
			changes = append(changes, ce)
		}
	}
	if p.cfg.MaxCheckpointFiles > 0 && len(changes) > p.cfg.MaxCheckpointFiles {
		return Manifest{}, nil, fmt.Errorf("%w: got %d files, limit %d", ErrOverlayFileLimitExceeded, len(changes), p.cfg.MaxCheckpointFiles)
	}

	entries := make([]ManifestEntry, 0, len(changes))
	var blobRefs []string
	seenLinkGroups := make(map[string]struct{})
	var changedBytes, baseBytes int64

	for _, ce := range changes {
		me, cBytes, bBytes, refs, err := p.persistChangeBlobs(ce, req.CandidateBases[ce.Path], seenLinkGroups)
		if err != nil {
			return Manifest{}, nil, err
		}
		changedBytes += cBytes
		baseBytes += bBytes
		blobRefs = append(blobRefs, refs...)
		entries = append(entries, me)
	}

	if err := inject(FaultAfterBlobWrite); err != nil {
		return Manifest{}, nil, err
	}

	metaBytes, err := estimateMetadataBytes(entries, req.SourceObservations, req.PrivateGit)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: metadata: %w", ErrInvalidManifest, err)
	}
	totalBytes := changedBytes + metaBytes + req.PrivateGit.RetainedObjectBytes
	if p.cfg.MaxCheckpointBytes > 0 && totalBytes > p.cfg.MaxCheckpointBytes {
		return Manifest{}, nil, fmt.Errorf("%w: got %d bytes, limit %d", ErrOverlayByteLimitExceeded, totalBytes, p.cfg.MaxCheckpointBytes)
	}

	triggers := make([]TriggerEvent, 0, len(batch))
	now := time.Now().UTC()
	for _, b := range batch {
		triggers = append(triggers, TriggerEvent{
			Kind:        b.req.Trigger,
			CommandID:   b.req.CommandID,
			BatchID:     b.req.BatchID,
			RequestedAt: now,
		})
	}

	manifest := Manifest{
		CheckpointID:       req.CheckpointID,
		FormatVersion:      FormatVersion,
		WorkerID:           p.cfg.WorkerID,
		TaskID:             p.cfg.TaskID,
		ProjectID:          p.cfg.ProjectID,
		SourceGeneration:   p.cfg.SourceGeneration,
		SelectedRoots:      append([]layeredfs.RootSpec(nil), p.cfg.SelectedRoots...),
		ParentCheckpoints:  append([]string(nil), p.cfg.ParentCheckpoints...),
		Trigger:            req.Trigger,
		CoalescedTriggers:  triggers,
		Entries:            entries,
		SourceObservations: append([]SourceObservation(nil), req.SourceObservations...),
		PrivateGit:         clonePrivateGit(req.PrivateGit),
		ResourceAccounting: ResourceAccounting{
			ChangedFiles:        len(entries),
			ChangedBytes:        changedBytes,
			BaseBytes:           baseBytes,
			MetadataBytes:       metaBytes,
			RetainedObjectBytes: req.PrivateGit.RetainedObjectBytes,
			TotalOverlayBytes:   totalBytes,
		},
		CreatedAt: now,
	}

	err = p.cfg.Store.stageManifest(manifest, func() error {
		return inject(FaultBeforeManifestSync)
	})
	if err != nil {
		return Manifest{}, nil, err
	}
	return manifest, uniqueSortedStrings(blobRefs), nil
}

func (p *Publisher) persistChangeBlobs(
	ce layeredfs.ChangeEntry,
	candidates []layeredfs.BaseCapture,
	seenGroups map[string]struct{},
) (ManifestEntry, int64, int64, []string, error) {
	var refs []string
	var cBytes, bBytes int64

	if !ce.Deleted && ce.Kind == layeredfs.EntryRegular {
		sha, err := p.writeWorkingNodeBlob(ce)
		if err != nil {
			return ManifestEntry{}, 0, 0, nil, err
		}
		ce.Final.SHA256 = sha
		refs = append(refs, sha)
		if ce.Final.HardLinkGroup == "" {
			cBytes = ce.Final.Size
		} else if _, seen := seenGroups[ce.Final.HardLinkGroup]; !seen {
			seenGroups[ce.Final.HardLinkGroup] = struct{}{}
			cBytes = ce.Final.Size
		}
	}

	if ce.Base.Kind == layeredfs.EntryRegular && (ce.Base.BackingFile != "" || ce.Base.SHA256 != "") {
		bSHA, err := p.persistBaseCaptureBlob(ce.Base)
		if err != nil {
			return ManifestEntry{}, 0, 0, nil, err
		}
		ce.Base.SHA256 = bSHA
		bBytes = ce.Base.Size
		refs = append(refs, bSHA)
	}

	var storedCandidates []layeredfs.BaseCapture
	for _, cand := range candidates {
		if cand.Kind == layeredfs.EntryRegular && (cand.BackingFile != "" || cand.SHA256 != "") {
			cSHA, err := p.persistBaseCaptureBlob(cand)
			if err != nil {
				return ManifestEntry{}, 0, 0, nil, err
			}
			cand.SHA256 = cSHA
			refs = append(refs, cSHA)
		}
		storedCandidates = append(storedCandidates, cand)
	}

	me := ManifestEntry{
		Path:              ce.Path,
		Kind:              ce.Kind,
		Deleted:           ce.Deleted,
		DirectoryReplaced: ce.DirectoryReplaced,
		ContentChanged:    ce.ContentChanged,
		MetadataChanged:   ce.MetadataChanged,
		Final:             ce.Final,
		Base:              ce.Base,
		CandidateBases:    storedCandidates,
	}
	return me, cBytes, bBytes, refs, nil
}

func (p *Publisher) writeWorkingNodeBlob(ce layeredfs.ChangeEntry) (string, error) {
	rc, err := p.cfg.Core.OpenBackingReader(ce.NodeID, ce.Generation)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	return p.cfg.Store.WriteBlob(rc, ce.Final.SHA256, ce.Final.Size)
}

func (p *Publisher) persistBaseCaptureBlob(base layeredfs.BaseCapture) (string, error) {
	if base.BackingFile != "" {
		bf, err := os.Open(base.BackingFile)
		if err != nil {
			return "", err
		}
		defer func() { _ = bf.Close() }()
		return p.cfg.Store.WriteBlob(bf, base.SHA256, base.Size)
	}
	if _, err := p.cfg.Store.VerifyBlob(base.SHA256, base.Size); err != nil {
		return "", err
	}
	return base.SHA256, nil
}

func clonePrivateGit(in PrivateGitSnapshot) PrivateGitSnapshot {
	out := in
	if len(in.Refs) > 0 {
		out.Refs = make(map[string]string, len(in.Refs))
		for k, v := range in.Refs {
			out.Refs[k] = v
		}
	}
	if len(in.Roots) > 0 {
		out.Roots = make(map[string]RootGitRefs, len(in.Roots))
		for id, root := range in.Roots {
			root.Refs = maps.Clone(root.Refs)
			out.Roots[id] = root
		}
	}
	out.ObjectCheckpoints = slices.Clone(in.ObjectCheckpoints)
	if len(in.RetainedObjects) > 0 {
		out.RetainedObjects = append([]string(nil), in.RetainedObjects...)
	}
	return out
}

func mergeBatchRequests(p *Publisher, batch []*pendingPublish) PublishRequest {
	first := batch[0].req
	if first.CheckpointID == "" {
		seq := p.seq.Add(1)
		first.CheckpointID = fmt.Sprintf("chk-%s-%06d", p.cfg.WorkerID, seq)
	}
	for i := 1; i < len(batch); i++ {
		r := batch[i].req
		first.SourceObservations = append(first.SourceObservations, r.SourceObservations...)
		if r.PrivateGit.HeadRef != "" {
			first.PrivateGit = r.PrivateGit
		}
		for k, v := range r.CandidateBases {
			if first.CandidateBases == nil {
				first.CandidateBases = make(map[layeredfs.QualifiedPath][]layeredfs.BaseCapture)
			}
			first.CandidateBases[k] = v
		}
		if r.FaultInjector != nil {
			first.FaultInjector = r.FaultInjector
		}
	}
	return first
}

func estimateMetadataBytes(entries []ManifestEntry, obs []SourceObservation, git PrivateGitSnapshot) (int64, error) {
	raw, err := json.Marshal(struct {
		E []ManifestEntry
		O []SourceObservation
		G PrivateGitSnapshot
	}{entries, obs, git})
	return int64(len(bytes.TrimSpace(raw))), err
}

func uniqueSortedStrings(in []string) []string {
	set := make(map[string]struct{}, len(in))
	for _, s := range in {
		if s != "" {
			set[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
