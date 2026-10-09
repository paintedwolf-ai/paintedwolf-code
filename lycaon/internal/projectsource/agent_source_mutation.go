package projectsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// The pending effect retains its durable recovery row.
type agentSourceMutation struct {
	service *SourceMutationService
	row     *sourceMutationRow
	release func()
}

func (m *agentSourceMutation) ID() string { return m.row.ID }

// PrepareEffect records intent before the caller applies the file change.
func (s *SourceMutationService) PrepareEffect(ctx context.Context, effect sourceeffect.Plan) (sourceeffect.Pending, error) {
	if s == nil || s.Journal.db == nil || s.recorder == nil || effect.Record.ProjectID == "" {
		return nil, fmt.Errorf("durable source mutation service required")
	}
	kind := string(effect.Record.Op)
	if kind != "create" && kind != "write" && kind != "rename" && kind != "delete" {
		return nil, fmt.Errorf("unsupported native source effect %q", kind)
	}
	effect.Record.BeforeSize = max(effect.Record.BeforeSize, int64(len(effect.Record.Before)))
	effect.Record.AfterSize = max(effect.Record.AfterSize, int64(len(effect.Record.After)))
	if len(effect.Record.Before) > sourceledger.MaxRevisionContentBytes {
		effect.Record.Before = nil
	}
	if len(effect.Record.After) > sourceledger.MaxRevisionContentBytes {
		effect.Record.After = nil
	}
	now := time.Now().UTC()
	id := effect.Record.OperationID
	if id == "" {
		id = uuid.NewString()
	}
	unlock, err := s.operationLocks.Acquire(ctx, id)
	if err != nil {
		return nil, err
	}
	retained := false
	var releasePath func()
	defer func() {
		if !retained {
			if releasePath != nil {
				releasePath()
			}
			unlock()
		}
	}()
	release := func() {
		if releasePath != nil {
			releasePath()
		}
		unlock()
	}
	claim := sourceMutationPlan{AgentEffect: &effect}
	releasePath, err = s.Paths.reserveSourcePlan(claim)
	if err != nil {
		return nil, err
	}
	// Stable IDs recover attribution if the parent progress marker is interrupted.
	effect.Record.OperationID = id
	digestEffect := effect
	digestEffect.Record.TS = time.Time{}
	digestEffect.Change.TS = time.Time{}
	input, err := json.Marshal(digestEffect)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(input)
	existing, found, err := s.Journal.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if found {
		if existing.InputDigest != hex.EncodeToString(digest[:]) {
			return nil, ErrSourceMutationConflict
		}
		sourcefeed.NoteHostWrite(effect.Change.AbsPath)
		sourcefeed.NoteHostWrite(effect.Change.FromAbsPath)
		retained = true
		return &agentSourceMutation{service: s, row: existing, release: release}, nil
	}
	effect.Record.OperationID = id
	effect.Record.TS = now
	effect.Change.TS = now
	plan := sourceMutationPlan{
		Kind: kind, ProjectID: effect.Record.ProjectID, Changed: true,
		AgentEffect: &effect, Response: json.RawMessage(`{}`),
	}
	row := &sourceMutationRow{ID: id, ProjectID: plan.ProjectID, Kind: kind,
		InputDigest: hex.EncodeToString(digest[:]), Plan: plan, Status: sourceMutationPrepared,
		CreatedAt: now, UpdatedAt: now}
	if err := s.Journal.insert(ctx, row); err != nil {
		return nil, fmt.Errorf("prepare file change journal: %w", err)
	}
	// Watchers can see the file effect before its attribution transaction.
	sourcefeed.NoteHostWrite(effect.Change.AbsPath)
	sourcefeed.NoteHostWrite(effect.Change.FromAbsPath)
	retained = true
	return &agentSourceMutation{service: s, row: row, release: release}, nil
}

// Finish records the effect's result even if its request was canceled.
func (m *agentSourceMutation) Finish(ctx context.Context, effectErr error) error {
	if m.release != nil {
		defer m.release()
		defer func() { m.release = nil }()
	}
	ctx = context.WithoutCancel(ctx)
	if m.row.Status == sourceMutationCommitted {
		return effectErr
	}
	if effectErr != nil {
		// Filesystem finalization can fail after the file change takes effect.
		if err := observeAgentEffect(m.row.Plan.AgentEffect); err != nil {
			m.row.Status, m.row.Error = sourceMutationDiverged, effectErr.Error()
			return errors.Join(effectErr, m.service.Journal.update(ctx, m.row))
		}
	}
	m.row.Status, m.row.Error = sourceMutationFileApplied, ""
	if err := m.service.Journal.update(ctx, m.row); err != nil {
		return &sourceeffect.AppliedError{OperationID: m.ID(), Cause: errors.Join(effectErr, err)}
	}
	if err := m.service.settlement.commit(ctx, m.row); err != nil {
		return &sourceeffect.AppliedError{OperationID: m.ID(), Cause: errors.Join(effectErr, err)}
	}
	if effectErr != nil {
		return &sourceeffect.AppliedError{OperationID: m.ID(), Cause: effectErr}
	}
	return nil
}

func (s *SourceMutationService) recoverAgentEffect(ctx context.Context, row *sourceMutationRow) error {
	if row.Status == sourceMutationPrepared {
		if err := observeAgentEffect(row.Plan.AgentEffect); err != nil {
			row.Status, row.Error = sourceMutationDiverged, "interrupted file effect could not be confirmed: "+err.Error()
			return s.Journal.update(ctx, row)
		}
	}
	// The applied marker preserves attribution across subsequent file edits.
	row.Status = sourceMutationFileApplied
	if err := s.Journal.update(ctx, row); err != nil {
		return err
	}
	return s.settlement.commit(ctx, row)
}

func observeAgentEffect(effect *sourceeffect.Plan) error {
	if effect.MetadataOnly {
		return fmt.Errorf("metadata effect has no durable completion marker")
	}
	if effect.Record.Op == api.SourceChangeOpDelete {
		file, err := fseffect.OpenRead(effect.Target)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			_ = file.Close()
			return fmt.Errorf("deleted path still exists")
		}
		return err
	}
	if effect.Record.Op == api.SourceChangeOpRename {
		file, err := fseffect.OpenRead(effect.From)
		if err == nil {
			_ = file.Close()
			return fmt.Errorf("rename source still exists")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	file, err := fseffect.OpenRead(effect.Target)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if effect.Record.EntryKind == sourceledger.EntryKindDirectory && info.IsDir() {
		return nil
	}
	if !info.Mode().IsRegular() || effect.Record.AfterSHA256 == "" {
		return fmt.Errorf("file effect has no verifiable content fingerprint")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != effect.Record.AfterSHA256 {
		return fmt.Errorf("file content differs from the prepared effect")
	}
	return nil
}

// Attribution and removal of duplicate journal bodies commit in one transaction.
func committedAgentEffectPlan(plan sourceMutationPlan) (any, error) {
	if plan.AgentEffect == nil {
		return nil, nil
	}
	effect := *plan.AgentEffect
	effect.Record.Before, effect.Record.After = nil, nil
	plan.AgentEffect = &effect
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}
