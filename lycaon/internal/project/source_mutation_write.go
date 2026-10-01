package project

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/textfile"
)

func (s *SourceMutationService) Write(ctx context.Context, operationID string, p *Project, req SourceWriteRequest) (*SourceWriteResult, error) {
	if p == nil || len(p.Roots) == 0 {
		return nil, ErrSourceNoRoot
	}
	digest, err := sourceMutationDigest(struct {
		ProjectID, Path, RootID, Content, Encoding, BaseSHA256, SessionID string
		Turn                                                              int
	}{p.ID, req.Path, req.RootID, req.Content, req.Encoding, strings.ToLower(strings.TrimSpace(req.BaseSHA256)), req.SessionID, req.Turn})
	if err != nil {
		return nil, err
	}
	response, err := s.execute(ctx, operationID, p.ID, digest, func() (*sourceMutationPlan, error) {
		writePlan, planErr := planProjectSourceWrite(p, req)
		if planErr != nil {
			return nil, planErr
		}
		wire := struct {
			Path, RootID, SHA256 string
			SizeBytes            int64
		}{writePlan.Result.Path, writePlan.Result.RootID, writePlan.Result.SHA256, writePlan.Result.SizeBytes}
		encoded, marshalErr := json.Marshal(wire)
		if marshalErr != nil {
			return nil, marshalErr
		}
		root, ok := rootByID(p.Roots, writePlan.Result.RootID)
		if !ok {
			return nil, ErrRootNotFound
		}
		return &sourceMutationPlan{
			Kind: "write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: writePlan.Result.RootID, RootPath: root.Path,
			Path: writePlan.Result.Path, AbsPath: writePlan.Result.AbsPath, Encoding: req.Encoding,
			BaseSHA256: writePlan.BaseSHA256, AfterSHA: writePlan.Result.SHA256,
			Before: writePlan.Result.Before, After: writePlan.Result.After, SessionID: req.SessionID, Turn: req.Turn,
			Changed:  writePlan.Result.Changed,
			Response: encoded,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	var wire struct {
		Path, RootID, SHA256 string
		SizeBytes            int64
	}
	if err := json.Unmarshal(response, &wire); err != nil {
		return nil, err
	}
	return &SourceWriteResult{Path: wire.Path, RootID: wire.RootID, SHA256: wire.SHA256, SizeBytes: wire.SizeBytes}, nil
}

// SourceBatchWritePlan captures a multi-file change and its response.
type SourceBatchWritePlan struct {
	Writes   []SourceWriteRequest
	Response json.RawMessage
}

// SourceBatchWriteRequest plans only after checking the operation receipt.
type SourceBatchWriteRequest struct {
	Input     any
	SessionID string
	Turn      int
	Prepare   func() (SourceBatchWritePlan, error)
}

// BatchWrite commits one recoverable multi-file mutation.
func (s *SourceMutationService) BatchWrite(ctx context.Context, operationID string, p *Project, req SourceBatchWriteRequest) (json.RawMessage, error) {
	if p == nil || len(p.Roots) == 0 {
		return nil, ErrSourceNoRoot
	}
	digest, err := sourceMutationDigest(struct {
		ProjectID string
		Input     any
		SessionID string
		Turn      int
	}{p.ID, req.Input, strings.TrimSpace(req.SessionID), req.Turn})
	if err != nil {
		return nil, err
	}
	return s.execute(ctx, operationID, p.ID, digest, func() (*sourceMutationPlan, error) {
		if req.Prepare == nil {
			return nil, fmt.Errorf("source batch prepare required")
		}
		prepared, prepareErr := req.Prepare()
		if prepareErr != nil {
			return nil, prepareErr
		}
		writes := make([]sourceMutationPlan, 0, len(prepared.Writes))
		for _, write := range prepared.Writes {
			write.SessionID, write.Turn = req.SessionID, req.Turn
			planned, planErr := planProjectSourceWrite(p, write)
			if planErr != nil {
				return nil, planErr
			}
			root, ok := rootByID(p.Roots, planned.Result.RootID)
			if !ok {
				return nil, ErrRootNotFound
			}
			writes = append(writes, sourceMutationPlan{
				Kind: "write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: planned.Result.RootID, RootPath: root.Path,
				Path: planned.Result.Path, AbsPath: planned.Result.AbsPath, Encoding: write.Encoding,
				BaseSHA256: planned.BaseSHA256, AfterSHA: planned.Result.SHA256,
				Before: planned.Result.Before, After: planned.Result.After, SessionID: req.SessionID, Turn: req.Turn,
				Changed: planned.Result.Changed,
			})
		}
		return &sourceMutationPlan{Kind: "batch_write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), BatchID: operationID,
			Writes: writes, Changed: len(writes) > 0, Response: append(json.RawMessage(nil), prepared.Response...)}, nil
	})
}

func (s *SourceMutationService) Create(ctx context.Context, operationID string, p *Project, req SourceEntryCreateRequest) (string, error) {
	if p == nil || len(p.Roots) == 0 {
		return "", ErrSourceNoRoot
	}
	releaseHistory, lockErr := s.lockSourceHistory(ctx, p.ID)
	if lockErr != nil {
		return "", lockErr
	}
	defer releaseHistory()

	digest, err := sourceMutationDigest(struct {
		ProjectID, Path, RootID, Kind, SessionID string
		Turn                                     int
	}{p.ID, req.Path, req.RootID, string(req.Kind), req.SessionID, req.Turn})
	if err != nil {
		return "", err
	}
	response, err := s.execute(ctx, operationID, p.ID, digest, func() (*sourceMutationPlan, error) {
		if req.Kind != SourceEntryFile && req.Kind != SourceEntryFolder {
			return nil, ErrSourceKindInvalid
		}
		pathQuery := strings.TrimSpace(req.Path)
		if pathQuery == "" {
			return nil, ErrSourcePathInvalid
		}
		abs, rel, resolveErr := resolveFreeSourcePath(p, req.RootID, pathQuery)
		if resolveErr != nil {
			return nil, resolveErr
		}
		root, resolveErr := selectSingleSourceRoot(p, req.RootID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		encoded, _ := json.Marshal(struct{ Path string }{rel})
		plan := &sourceMutationPlan{Kind: "create", RecoveryID: operationID, ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: root.ID, RootPath: root.Path,
			Path: rel, AbsPath: abs, EntryKind: req.Kind, SessionID: req.SessionID, Turn: req.Turn, Changed: true, Response: encoded}
		if req.Kind == SourceEntryFile {
			plan.After, plan.AfterSHA = []byte{}, textfile.SHA256(nil)
		}
		return plan, nil
	})
	if err != nil {
		return "", err
	}
	var wire struct{ Path string }
	if err := json.Unmarshal(response, &wire); err != nil {
		return "", err
	}
	return wire.Path, nil
}
