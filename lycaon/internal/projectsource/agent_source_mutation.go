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
	pending, err := s.prepareAgentEffect(ctx, effect)
	if err != nil {
		return nil, err
	}
	if err := s.Journal.insert(ctx, pending.row); err != nil {
		pending.release()
		return nil, fmt.Errorf("prepare file change journal: %w", err)
	}
	notePreparedAgentEffect(pending.row)
	return pending, nil
}

func notePreparedAgentEffect(row *sourceMutationRow) {
	sourcefeed.NoteHostWrite(row.Plan.AgentEffect.Change.AbsPath)
	sourcefeed.NoteHostWrite(row.Plan.AgentEffect.Change.FromAbsPath)
}

func (s *SourceMutationService) prepareAgentEffect(ctx context.Context, effect sourceeffect.Plan) (*agentSourceMutation, error) {
	return s.prepareAgentEffectOpt(ctx, effect, true)
}

func (s *SourceMutationService) prepareAgentEffectOpt(ctx context.Context, effect sourceeffect.Plan, reservePath bool) (*agentSourceMutation, error) {
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
	freshID := id == ""
	if freshID {
		id = uuid.Must(uuid.NewV7()).String()
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
	if reservePath {
		claim := sourceMutationPlan{AgentEffect: &effect}
		releasePath, err = s.Paths.reserveSourcePlan(claim)
		if err != nil {
			return nil, err
		}
	}
	// The operation ID identifies one admitted filesystem effect.
	effect.Record.OperationID = id
	var digestHex string
	if !freshID || reservePath {
		digestEffect := effect
		digestEffect.Record.TS = time.Time{}
		digestEffect.Change.TS = time.Time{}
		input, err := json.Marshal(digestEffect)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(input)
		digestHex = hex.EncodeToString(digest[:])
		if !freshID {
			existing, found, err := s.Journal.load(ctx, id)
			if err != nil {
				return nil, err
			}
			if found {
				if existing.InputDigest != digestHex {
					return nil, ErrSourceMutationConflict
				}
				return nil, fmt.Errorf("%w: operation %s already admitted", ErrSourceMutationConflict, id)
			}
		}
	}
	effect.Record.OperationID = id
	effect.Record.TS = now
	effect.Change.TS = now
	plan := sourceMutationPlan{
		sourceMutationAttribution: sourceMutationAttribution{ProjectID: effect.Record.ProjectID},
		Kind:                      kind,
		Changed:                   true,
		AgentEffect:               &effect,
		Response:                  json.RawMessage(`{}`),
	}
	row := &sourceMutationRow{freshOperation: true, ID: id, ProjectID: plan.ProjectID, Kind: kind,
		InputDigest: digestHex, Plan: plan, Status: sourceMutationPrepared,
		CreatedAt: now, UpdatedAt: now}
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
	if effect.Record.EntryKind == sourceledger.EntryKindLink && effect.Record.Op != api.SourceChangeOpDelete {
		root, err := os.OpenRoot(effect.Target.Root)
		if err != nil {
			return err
		}
		defer func() { _ = root.Close() }()
		target, err := root.Readlink(effect.Target.Rel)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(target))
		if hex.EncodeToString(digest[:]) != effect.Record.AfterSHA256 {
			return fmt.Errorf("link target differs from the prepared effect")
		}
		return nil
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
