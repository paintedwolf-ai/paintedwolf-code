package projectsource

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

// SourceVersions verifies retained states before mutation admission.
type SourceVersions struct {
	recovery *sourceRecovery
	heads    SourceHeadReader
	recorder SourceMutationRecorder
	execute  func(context.Context, string, string, string, func() (*sourceMutationPlan, error)) (json.RawMessage, error)
}

// SourceVersionRestoreRequest selects the state to make current.
type SourceVersionRestoreRequest struct {
	Version sourceledger.RestorableVersion
	// Commit selects revision-backed provenance.
	Commit    string
	FileID    string
	RootID    string
	Path      string
	Base      api.SourceTip
	SessionID string
	Turn      int
}

// SourceVersionRestoreResult reports the new working-file state.
type SourceVersionRestoreResult struct {
	VersionID         string
	PreviousVersionID string
	FileID            string
	RootID            string
	Path              string
	State             api.SourceTipState
	SHA256            string
	Changed           bool
}

// RestoreVersion makes a retained state current and records its provenance.
func (s *SourceVersions) Restore(
	ctx context.Context,
	operationID string,
	p ProjectSource,
	req SourceVersionRestoreRequest,
) (*SourceVersionRestoreResult, error) {
	if p == nil || len(p.SourceRoots()) == 0 {
		return nil, ErrSourceNoRoot
	}
	commit := strings.TrimSpace(req.Commit)
	if (req.Version.ID == "") == (commit == "") {
		return nil, sourceledger.ErrHistoryNotFound
	}
	if req.Version.ProjectID != p.SourceID() ||
		strings.TrimSpace(req.FileID) == "" || req.Version.FileID != strings.TrimSpace(req.FileID) {
		return nil, sourceledger.ErrHistoryNotFound
	}
	if req.Version.State != string(api.SourceTipStateContent) &&
		req.Version.State != string(api.SourceTipStateAbsent) {
		return nil, sourceledger.ErrVersionUnavailable
	}
	if req.Version.State == string(api.SourceTipStateContent) &&
		(req.Version.SHA256 == "" || textfile.SHA256(req.Version.Content) != req.Version.SHA256) {
		return nil, sourceledger.ErrVersionUnavailable
	}

	digest, err := sourceMutationDigest(struct {
		ProjectID, VersionID, Commit, FileID, RootID, Path, BaseState, BaseSHA256, SessionID string
		Turn                                                                                 int
	}{
		p.SourceID(), req.Version.ID, commit, req.FileID, req.RootID, req.Path,
		string(req.Base.State), strings.ToLower(strings.TrimSpace(req.Base.Sha256)),
		req.SessionID, req.Turn,
	})
	if err != nil {
		return nil, err
	}
	response, err := s.execute(ctx, operationID, p.SourceID(), digest, func() (*sourceMutationPlan, error) {
		return s.prepareSourceVersionRestore(ctx, operationID, p, req)
	})
	if err != nil {
		return nil, err
	}
	var result SourceVersionRestoreResult
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *SourceVersions) prepareSourceVersionRestore(
	ctx context.Context,
	operationID string,
	p ProjectSource,
	req SourceVersionRestoreRequest,
) (*sourceMutationPlan, error) {
	baseState := req.Base.State
	if baseState != api.SourceTipStateContent && baseState != api.SourceTipStateAbsent {
		return nil, ErrSourcePathInvalid
	}
	if baseState == api.SourceTipStateContent && strings.TrimSpace(req.Base.Sha256) == "" {
		return nil, ErrSourcePathInvalid
	}

	var plan *sourceMutationPlan
	var err error
	if baseState == api.SourceTipStateAbsent {
		plan, err = prepareRestoreOverAbsent(p, req)
	} else {
		plan, err = prepareRestoreOverContent(ctx, operationID, p, req)
	}
	if err != nil {
		return nil, err
	}
	if plan.Kind == "delete" {
		if err := s.recovery.captureRecovery(ctx, plan, plan.AbsPath); err != nil {
			return nil, err
		}
	}
	plan.FileID = req.FileID
	plan.DerivedFromVersionID = req.Version.ID
	plan.Cause = sourceledger.CauseVersionRestore
	if strings.TrimSpace(req.Commit) != "" {
		plan.Cause = sourceledger.CauseCommitRestore
	}
	plan.SessionID, plan.Turn = req.SessionID, req.Turn
	previousVersionID, err := s.captureRestoreBaseVersion(ctx, p, req, plan)
	if err != nil {
		return nil, err
	}
	result := SourceVersionRestoreResult{
		VersionID: req.Version.ID, FileID: req.FileID,
		PreviousVersionID: previousVersionID,
		RootID:            plan.RootID, Path: plan.Path,
		State: api.SourceTipState(req.Version.State), SHA256: req.Version.SHA256,
		Changed: plan.Changed,
	}
	plan.Response, err = json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *SourceVersions) captureRestoreBaseVersion(
	ctx context.Context,
	p ProjectSource,
	req SourceVersionRestoreRequest,
	plan *sourceMutationPlan,
) (string, error) {
	if s.recorder == nil {
		return "", nil
	}
	if req.Base.State == api.SourceTipStateAbsent {
		head, err := s.heads.ResolveHeadByFile(ctx, p.SourceID(), sourcebranch.Trunk, req.FileID)
		if errors.Is(err, sourceledger.ErrHistoryNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if head.FileID != req.FileID {
			return "", ErrSourceWriteConflict
		}
		if head.RootID != plan.RootID || head.Path != plan.Path {
			return "", ErrSourceWriteConflict
		}
		if head.State != string(api.SourceTipStateAbsent) {
			if err := s.recorder.Record(ctx, sourceledger.RecordInput{
				ProjectID: p.SourceID(),
				RootID:    plan.RootID, Path: plan.Path, FileID: req.FileID,
				EntryKind: sourceledger.EntryKindFile,
				Op:        api.SourceChangeOpDelete, Origin: api.SourceChangeOriginExternal,
				BeforeSHA256: head.SHA256, Cause: sourceledger.CauseVersionRestoreBase,
			}); err != nil {
				return "", err
			}
			head, err = s.heads.ResolveHeadByFile(ctx, p.SourceID(), sourcebranch.Trunk, req.FileID)
			if err != nil {
				return "", err
			}
		}
		return head.VersionID, nil
	}
	if plan.BeforeSize > sourceledger.MaxRevisionContentBytes {
		return "", sourceledger.ErrVersionUnavailable
	}
	head, err := s.heads.ResolveHeadByFile(ctx, p.SourceID(), sourcebranch.Trunk, req.FileID)
	if err != nil && !errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return "", err
	}
	if err == nil && (head.RootID != plan.RootID || head.Path != plan.Path) {
		return "", ErrSourceWriteConflict
	}
	if err == nil && head.State == string(api.SourceTipStateContent) && head.SHA256 == plan.BaseSHA256 {
		return head.VersionID, nil
	}
	beforeSHA := ""
	if err == nil && head.State == string(api.SourceTipStateContent) {
		beforeSHA = head.SHA256
	}
	if err := s.recorder.Record(ctx, sourceledger.RecordInput{
		ProjectID: p.SourceID(),
		RootID:    plan.RootID, Path: plan.Path, FileID: req.FileID,
		EntryKind: sourceledger.EntryKindFile,
		Op:        api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal,
		BeforeSHA256: beforeSHA,
		AfterSHA256:  plan.BaseSHA256, After: plan.Before, AfterSize: plan.BeforeSize,
		Cause: sourceledger.CauseVersionRestoreBase,
	}); err != nil {
		return "", err
	}
	head, err = s.heads.ResolveHeadByFile(ctx, p.SourceID(), sourcebranch.Trunk, req.FileID)
	if err != nil {
		return "", err
	}
	return head.VersionID, nil
}

func prepareRestoreOverAbsent(
	p ProjectSource,
	req SourceVersionRestoreRequest,
) (*sourceMutationPlan, error) {
	abs, rel, err := resolveFreeSourcePath(p, req.RootID, req.Path)
	if err != nil {
		if errors.Is(err, ErrSourceExists) {
			return nil, ErrSourceWriteConflict
		}
		return nil, err
	}
	root, err := selectSingleSourceRoot(p, req.RootID)
	if err != nil {
		return nil, err
	}
	plan := &sourceMutationPlan{
		sourceMutationRecovery:    sourceMutationRecovery{NativeTrash: &sourceTrashRecovery{}},
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.SourceID(), WorkspaceID: p.WorkspaceID()},
		Kind:                      "create",
		RootID:                    root.ID,
		RootPath:                  root.Path,
		Path:                      rel,
		AbsPath:                   abs,
		EntryKind:                 SourceEntryFile,
		CreateParents:             true,
	}
	if req.Version.State == string(api.SourceTipStateAbsent) {
		plan.Kind = "write"
		return plan, nil
	}
	plan.After = append([]byte(nil), req.Version.Content...)
	plan.AfterSHA = req.Version.SHA256
	plan.AfterSize = int64(len(plan.After))
	plan.Changed = true
	return plan, nil
}

func prepareRestoreOverContent(
	ctx context.Context,
	operationID string,
	p ProjectSource,
	req SourceVersionRestoreRequest,
) (*sourceMutationPlan, error) {
	root, abs, rel, err := locateLifecycleSource(p, req.RootID, req.Path)
	if errors.Is(err, ErrSourceNotFound) {
		return nil, ErrSourceWriteConflict
	}
	if err != nil {
		return nil, err
	}
	before, err := sourceHistoryEvidence(ctx, abs)
	if err != nil {
		return nil, err
	}
	if before.entryKind != SourceEntryFile || before.sha256 != strings.ToLower(strings.TrimSpace(req.Base.Sha256)) {
		return nil, ErrSourceWriteConflict
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrSourceWriteConflict
	}
	if req.Version.State == string(api.SourceTipStateAbsent) {
		treeSHA, fingerprintErr := sourceTreeFingerprint(ctx, abs)
		if fingerprintErr != nil {
			return nil, fingerprintErr
		}
		return &sourceMutationPlan{
			sourceMutationContent:     sourceMutationContent{Before: before.content, BaseSHA256: before.sha256, BeforeSize: before.size},
			sourceMutationRecovery:    sourceMutationRecovery{RecoveryID: operationID, TreeSHA: treeSHA, Disposal: sourceDisposalTrash},
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.SourceID(), WorkspaceID: p.WorkspaceID()},
			Kind:                      "delete",
			RootID:                    root.ID,
			RootPath:                  root.Path,
			Path:                      rel,
			AbsPath:                   abs,
			EntryKind:                 SourceEntryFile,
			Changed:                   true,
		}, nil
	}
	changed := before.sha256 != req.Version.SHA256
	return &sourceMutationPlan{
		sourceMutationContent:     sourceMutationContent{Before: before.content, BaseSHA256: before.sha256, BeforeSize: before.size, After: append([]byte(nil), req.Version.Content...), AfterSHA: req.Version.SHA256, AfterSize: int64(len(req.Version.Content))},
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.SourceID(), WorkspaceID: p.WorkspaceID()},
		Kind:                      "write",
		RootID:                    root.ID,
		RootPath:                  root.Path,
		Path:                      rel,
		AbsPath:                   abs,
		EntryKind:                 SourceEntryFile,
		Changed:                   changed,
	}, nil
}
