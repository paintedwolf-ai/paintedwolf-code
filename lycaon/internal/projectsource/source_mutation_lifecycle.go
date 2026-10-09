package projectsource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
)

func (s *SourceMutationService) Rename(ctx context.Context, operationID string, p ProjectSource, req SourceRenameRequest) (*SourceLifecycleResult, error) {
	return s.lifecycle(ctx, operationID, p, "rename", req.RootID, req.From, req.To, false, req.SessionID, req.Turn, req.Prepare)
}

func (s *SourceMutationService) Copy(ctx context.Context, operationID string, p ProjectSource, req SourceCopyRequest) (*SourceLifecycleResult, error) {
	return s.lifecycle(ctx, operationID, p, "copy", req.RootID, req.From, req.To, false, req.SessionID, req.Turn, nil)
}

func (s *SourceMutationService) lifecycle(ctx context.Context, operationID string, p ProjectSource, kind, rootID, from, to string, recursive bool, sessionID string, turn int, prepareRename func(context.Context, SourceRenamePlan) error) (*SourceLifecycleResult, error) {
	if p == nil || len(p.SourceRoots()) == 0 {
		return nil, ErrSourceNoRoot
	}
	releaseHistory, lockErr := s.Paths.lockSourceHistory(ctx, p.SourceID())
	if lockErr != nil {
		return nil, lockErr
	}
	defer releaseHistory()

	digest, err := sourceMutationDigest(struct {
		ProjectID, Kind, RootID, From, To, SessionID string
		Recursive                                    bool
		Turn                                         int
	}{p.SourceID(), kind, rootID, from, to, sessionID, recursive, turn})
	if err != nil {
		return nil, err
	}
	response, err := s.execute(ctx, operationID, p.SourceID(), digest, func() (*sourceMutationPlan, error) {
		root, fromAbs, fromRel, locateErr := locateLifecycleSource(p, rootID, from)
		if locateErr != nil {
			return nil, locateErr
		}
		toAbs, toRel, resolveErr := resolveLifecycleDest(root, to)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if prepareRename != nil {
			if err := prepareRename(ctx, SourceRenamePlan{OperationID: operationID, RootID: root.ID, From: fromRel, To: toRel}); err != nil {
				return nil, err
			}
		}
		identity, identityErr := fspath.EntryIdentity(fromAbs)
		if identityErr != nil {
			return nil, identityErr
		}
		encoded, _ := json.Marshal(SourceLifecycleResult{RootID: root.ID, Path: toRel})
		plan := &sourceMutationPlan{Kind: kind, ProjectID: p.SourceID(), WorkspaceID: p.WorkspaceID(), RootID: root.ID, RootPath: root.Path,
			Path: toRel, FromPath: fromRel, ToPath: toRel, FromAbs: fromAbs, ToAbs: toAbs,
			EntryIdentity: identity, SessionID: sessionID, Turn: turn, Changed: true, Response: encoded}
		info, statErr := os.Lstat(fromAbs)
		if statErr != nil {
			return nil, statErr
		}
		plan.EntryKind = sourceEntryKind(info)
		plan.AfterSize = info.Size()
		if kind == "rename" {
			plan.BeforeSize = info.Size()
		}
		if kind == "copy" {
			plan.StageAbs = filepath.Join(filepath.Dir(toAbs), ".paintedwolf-copy-"+operationID)
			if _, statErr := os.Lstat(plan.StageAbs); statErr == nil {
				return nil, ErrSourceExists
			} else if !os.IsNotExist(statErr) {
				return nil, statErr
			}
		}
		return plan, nil
	})
	if err != nil {
		return nil, err
	}
	var result SourceLifecycleResult
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *SourceMutationService) Delete(ctx context.Context, operationID string, p ProjectSource, req SourceDeleteRequest) error {
	if p == nil || len(p.SourceRoots()) == 0 {
		return ErrSourceNoRoot
	}
	if s == nil {
		return fmt.Errorf("source mutation service unavailable")
	}
	if err := s.validateLifecycleReplay(ctx, operationID, p); err != nil {
		return err
	}
	releaseHistory, lockErr := s.Paths.lockSourceHistory(ctx, p.SourceID())
	if lockErr != nil {
		return lockErr
	}
	defer releaseHistory()

	digest, err := sourceMutationDigest(struct {
		ProjectID, RootID, Path, SessionID string
		Recursive                          bool
	}{p.SourceID(), req.RootID, req.Path, req.SessionID, req.Recursive})
	if err != nil {
		return err
	}
	_, err = s.execute(ctx, operationID, p.SourceID(), digest, func() (*sourceMutationPlan, error) {
		root, abs, rel, locateErr := locateLifecycleSource(p, req.RootID, req.Path)
		if locateErr != nil {
			return nil, locateErr
		}
		info, statErr := os.Lstat(abs)
		if statErr != nil {
			return nil, statErr
		}
		if info.IsDir() && !req.Recursive {
			entries, readErr := os.ReadDir(abs)
			if readErr != nil {
				return nil, readErr
			}
			if len(entries) > 0 {
				return nil, ErrSourceNotEmpty
			}
		}
		plan := &sourceMutationPlan{Kind: "delete", ProjectID: p.SourceID(), WorkspaceID: p.WorkspaceID(), RootID: root.ID, RootPath: root.Path,
			Path: rel, AbsPath: abs, NativeTrash: &sourceTrashRecovery{}, Recursive: req.Recursive, Disposal: sourceDisposalTrash,
			EntryKind: sourceEntryKind(info), BeforeSize: info.Size(),
			SessionID: req.SessionID, Turn: req.Turn,
			Changed: true, Response: json.RawMessage(`{}`)}
		plan.EntryIdentity, statErr = fspath.EntryIdentity(abs)
		if statErr != nil { return nil, statErr }
		return plan, nil
	})
	return err
}
