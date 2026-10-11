package checkpointcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

// Rewind errors map to API responses.
var (
	// ErrSessionNotIdle rejects rewinds during live work.
	ErrSessionNotIdle = errors.New("session not idle")
	// ErrRewindAnchorNotFound reports an anchor id absent from the transcript.
	ErrRewindAnchorNotFound = errors.New("rewind anchor not found")
	// ErrRewindAnchorIneligible reports a row that exists but is not a user ask.
	ErrRewindAnchorIneligible = errors.New("rewind anchor ineligible")
)

// RewindResult reports what a rewind changed.
type RewindResult struct {
	RestoredPaths         []string
	TruncatedMessageCount int
	// RestoredPrompt excludes generated attachment content.
	RestoredPrompt string
	// RestoredContentParts preserve structured prompt content.
	RestoredContentParts []api.MessageContentPart
	// RestoredArtifactIDs are visual artifacts linked on the anchor ask.
	RestoredArtifactIDs []string
}

var ErrRewindOperationConflict = errors.New("rewind operation id was already used for different input")

// RewindToPrompt undoes the source contributions from an idle session's prompt onward.
func (m *Rewinds) RewindToPrompt(ctx context.Context, operationID, sessionID, anchorMessageID, planDigest string) (*RewindResult, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	operationID = strings.TrimSpace(operationID)
	sessionID = strings.TrimSpace(sessionID)
	anchorMessageID = strings.TrimSpace(anchorMessageID)
	if operationID == "" || sessionID == "" || anchorMessageID == "" {
		return nil, ErrRewindAnchorNotFound
	}
	digest := rewindInputDigest(sessionID, anchorMessageID)
	if replayed, ok, err := m.replayRewind(ctx, operationID, sessionID, anchorMessageID, digest); err != nil || ok {
		return replayed, err
	}
	lock := m.prompt.Acquire(sessionID)
	if !lock.TryLock() {
		return nil, ErrSessionNotIdle
	}
	defer lock.Unlock()
	// Recheck after acquiring the operation gate.
	if replayed, ok, err := m.replayRewind(ctx, operationID, sessionID, anchorMessageID, digest); err != nil || ok {
		return replayed, err
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := m.assertRewindIdle(ctx, sess); err != nil {
		return nil, err
	}

	msgs, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	anchor, ok := findMessage(msgs, anchorMessageID)
	if !ok {
		return nil, ErrRewindAnchorNotFound
	}
	if !api.IsUserIntentMessage(anchor) {
		return nil, ErrRewindAnchorIneligible
	}

	result := &RewindResult{
		RestoredPrompt:       api.MessageUserInstructionContent(anchor),
		RestoredContentParts: append([]api.MessageContentPart(nil), anchor.ContentParts...),
		RestoredArtifactIDs:  append([]string(nil), anchor.ArtifactIDs...),
	}

	// Persist checkpoint anchor IDs before truncation removes their messages.
	cpStore, err := m.captures.ForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	rootID := sessiontree.RootID(ctx, m.store, sessionID)
	anchorIDs := eligibleAnchorIDsFrom(msgs, anchorMessageID)
	removed, err := m.executeRewind(ctx, cpStore, rootID, operationID, digest, sessionID, anchorMessageID, anchorIDs, planDigest, result)
	if err != nil {
		return nil, err
	}
	result.TruncatedMessageCount = removed

	m.rollbackEphemeralState(ctx, sess)
	m.captures.Capture.DropAnchors(ctx, cpStore, rootID, anchorIDs)
	m.publishRewound(ctx, sessionID)
	return result, nil
}

func rewindInputDigest(sessionID, anchorMessageID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + anchorMessageID + "\x00before_turn"))
	return hex.EncodeToString(sum[:])
}

// rewindResultAPI preserves an empty restored_paths array on the wire.
func rewindResultAPI(result *RewindResult) api.RewindSessionResponse {
	return api.RewindSessionResponse{
		RestoredPaths:         append([]string{}, result.RestoredPaths...),
		TruncatedMessageCount: result.TruncatedMessageCount,
		RestoredPrompt:        result.RestoredPrompt,
		RestoredContentParts:  append([]api.MessageContentPart(nil), result.RestoredContentParts...),
		RestoredArtifactIDs:   append([]string(nil), result.RestoredArtifactIDs...),
	}
}

func rewindResultFromAPI(response api.RewindSessionResponse) *RewindResult {
	return &RewindResult{
		RestoredPaths:         append([]string{}, response.RestoredPaths...),
		TruncatedMessageCount: response.TruncatedMessageCount,
		RestoredPrompt:        response.RestoredPrompt,
		RestoredContentParts:  append([]api.MessageContentPart(nil), response.RestoredContentParts...),
		RestoredArtifactIDs:   append([]string(nil), response.RestoredArtifactIDs...),
	}
}

func (m *Rewinds) replayRewind(ctx context.Context, operationID, sessionID, anchorMessageID, digest string) (*RewindResult, bool, error) {
	op, err := m.store.GetRewindOperation(ctx, operationID)
	if err != nil || op == nil {
		return nil, false, err
	}
	if op.SessionID != sessionID || op.AnchorMessageID != anchorMessageID || op.InputDigest != digest {
		return nil, true, ErrRewindOperationConflict
	}
	if op.Status == "rolled_back" {
		return nil, false, nil
	}
	if op.Status != "committed" {
		return nil, true, fmt.Errorf("rewind operation %s is %s", operationID, op.Status)
	}
	var response api.RewindSessionResponse
	if err := json.Unmarshal([]byte(op.ResponseJSON), &response); err != nil {
		return nil, true, fmt.Errorf("decode rewind operation receipt: %w", err)
	}
	return rewindResultFromAPI(response), true, nil
}

func (m *Rewinds) executeRewind(ctx context.Context, cpStore *sessioncheckpoint.Store, rootID, operationID, inputDigest, sessionID, anchorMessageID string, anchorIDs []string, planDigest string, result *RewindResult) (int, error) {
	if cpStore == nil {
		return 0, fmt.Errorf("rewind checkpoint store unavailable")
	}
	man, err := cpStore.Load(ctx, rootID, anchorMessageID)
	if errors.Is(err, store.ErrCheckpointMissing) {
		man = &sessioncheckpoint.Manifest{SessionID: rootID, AnchorMessageID: anchorMessageID, ProjectDir: cpStore.ProjectDir()}
	} else if err != nil {
		return 0, err
	}
	journal, err := m.prepareSourceRewindJournal(ctx, cpStore, man, operationID, sessionID, anchorIDs)
	if err != nil {
		return 0, err
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	currentDigest, err := rewindPlanDigest(messages, journal.Source)
	if err != nil {
		_ = journal.Cleanup(cpStore)
		return 0, err
	}
	if planDigest == "" || currentDigest != planDigest {
		_ = journal.Cleanup(cpStore)
		return 0, ErrRewindPlanChanged
	}
	err = m.store.PrepareRewind(ctx, store.RewindOperation{
		ID: operationID, SessionID: sessionID, AnchorMessageID: anchorMessageID,
		InputDigest: inputDigest, ProjectDir: cpStore.ProjectDir(), JournalPath: journal.Path(), Status: string(sessioncheckpoint.PhasePrepared),
		CheckpointAnchorIDs: anchorIDs,
	})
	if err != nil {
		return 0, err
	}
	if err := m.store.SetRewindPhase(ctx, operationID, string(sessioncheckpoint.PhaseApplying), ""); err != nil {
		return 0, err
	}
	rollback := func(cause error) error {
		rollbackErr := journal.Rollback()
		status := "rolled_back"
		detail := cause.Error()
		if rollbackErr != nil {
			status = "diverged"
			detail = rollbackErr.Error()
		}
		_ = m.store.SetRewindPhase(ctx, operationID, status, detail)
		if rollbackErr != nil {
			return errors.Join(cause, rollbackErr)
		}
		return cause
	}
	restored, err := applyRewindJournal(journal)
	if err != nil {
		return 0, rollback(err)
	}
	result.RestoredPaths = restored
	if err := m.store.SetRewindPhase(ctx, operationID, string(sessioncheckpoint.PhaseFilesApplied), ""); err != nil {
		return 0, rollback(err)
	}
	removed, err := m.store.CommitRewind(ctx, operationID, sessionID, anchorMessageID, rewindResultAPI(result))
	if err != nil {
		// Resolve an ambiguous commit before changing the filesystem.
		committed, getErr := m.store.GetRewindOperation(ctx, operationID)
		if getErr != nil {
			return 0, errors.Join(err, fmt.Errorf("resolve ambiguous rewind commit: %w", getErr))
		}
		if committed == nil {
			return 0, errors.Join(err, fmt.Errorf("resolve ambiguous rewind commit: receipt missing"))
		}
		if committed.Status == "committed" {
			var response api.RewindSessionResponse
			if decodeErr := json.Unmarshal([]byte(committed.ResponseJSON), &response); decodeErr != nil {
				return 0, decodeErr
			}
			*result = *rewindResultFromAPI(response)
			_ = journal.Cleanup(cpStore)
			return response.TruncatedMessageCount, nil
		}
		return 0, rollback(err)
	}
	_ = journal.Cleanup(cpStore)
	return removed, nil
}

// RecoverRewinds resolves interrupted rewind operations at boot, then sweeps
// committed operations with pending journal or checkpoint cleanup.
func (m *Rewinds) RecoverRewinds(ctx context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager not configured")
	}
	operations, err := m.store.RewindOperationsForRecovery(ctx)
	if err != nil {
		return err
	}
	recoveryErr := m.resolveRewindOperations(ctx, operations)
	return errors.Join(recoveryErr, m.sweepCommittedRewinds(ctx))
}

// RecoverRewindsForSession resolves one session's interrupted rewinds.
// Committed-operation cleanup is boot-scoped.
func (m *Rewinds) RecoverRewindsForSession(ctx context.Context, sessionID string) error {
	if m == nil || m.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	operations, err := m.store.RewindOperationsForRecoverySession(ctx, sessionID)
	if err != nil {
		return err
	}
	return m.resolveRewindOperations(ctx, operations)
}

func (m *Rewinds) resolveRewindOperations(ctx context.Context, operations []store.RewindOperation) error {
	// Missing recovery storage leaves operation receipts unchanged.
	if len(operations) > 0 && strings.TrimSpace(m.captures.dataDir) == "" {
		return fmt.Errorf("recover rewinds: %w", ErrCheckpointRootUnset)
	}
	var recoveryErr error
	for _, op := range operations {
		recoveryErr = errors.Join(recoveryErr, m.resolveOneRewindOperation(ctx, op))
	}
	return recoveryErr
}

// Applied files recover forward; earlier phases roll back.
func (m *Rewinds) resolveOneRewindOperation(ctx context.Context, op store.RewindOperation) error {
	// Each journal resolves against its project checkpoint store.
	checkpoint := sessioncheckpoint.New(m.captures.dataDir, op.ProjectDir, m.store)
	if checkpoint == nil {
		err := fmt.Errorf("rewind operation %s names no project root", op.ID)
		_ = m.store.SetRewindPhase(ctx, op.ID, "diverged", err.Error())
		return err
	}
	journal, loadErr := sessioncheckpoint.LoadJournal(op.JournalPath)
	if loadErr != nil {
		_ = m.store.SetRewindPhase(ctx, op.ID, "diverged", loadErr.Error())
		return fmt.Errorf("recover rewind %s: %w", op.ID, loadErr)
	}
	if journal.ID != op.ID || journal.TranscriptID != op.SessionID || journal.AnchorMessageID != op.AnchorMessageID || filepath.Clean(journal.ProjectDir) != filepath.Clean(op.ProjectDir) {
		err := fmt.Errorf("rewind operation identity diverged")
		_ = m.store.SetRewindPhase(ctx, op.ID, "diverged", err.Error())
		return fmt.Errorf("recover rewind %s: %w", op.ID, err)
	}
	if err := m.bindSourceRewindJournal(ctx, journal); err != nil {
		return err
	}
	if op.Status == string(sessioncheckpoint.PhaseFilesApplied) || journal.Phase == sessioncheckpoint.PhaseFilesApplied {
		return m.completeRecoveredRewind(ctx, op, journal, checkpoint)
	}
	if err := journal.Rollback(); err != nil {
		_ = m.store.SetRewindPhase(ctx, op.ID, "diverged", err.Error())
		return fmt.Errorf("rollback rewind %s: %w", op.ID, err)
	}
	if err := m.store.SetRewindPhase(ctx, op.ID, "rolled_back", "recovered interrupted rewind"); err != nil {
		return fmt.Errorf("resolve rewind %s: %w", op.ID, err)
	}
	return nil
}

// Commit applied files before reclaiming unreachable checkpoint anchors.
func (m *Rewinds) completeRecoveredRewind(ctx context.Context, op store.RewindOperation, journal *sessioncheckpoint.Journal, checkpoint *sessioncheckpoint.Store) error {
	if _, err := journal.Apply(); err != nil {
		return err
	}
	if err := journal.VerifyTargets(); err != nil {
		_ = m.store.SetRewindPhase(ctx, op.ID, "diverged", err.Error())
		return fmt.Errorf("verify rewind %s: %w", op.ID, err)
	}
	if op.Status != string(sessioncheckpoint.PhaseFilesApplied) {
		if err := m.store.SetRewindPhase(ctx, op.ID, string(sessioncheckpoint.PhaseFilesApplied), ""); err != nil {
			return fmt.Errorf("advance rewind %s: %w", op.ID, err)
		}
	}
	response, err := m.rewindRecoveryResponse(ctx, op, journal)
	if err != nil {
		return fmt.Errorf("build rewind %s receipt: %w", op.ID, err)
	}
	if _, err := m.store.CommitRewind(ctx, op.ID, op.SessionID, op.AnchorMessageID, response); err != nil {
		return fmt.Errorf("commit rewind %s: %w", op.ID, err)
	}
	if err := journal.Cleanup(checkpoint); err != nil {
		return fmt.Errorf("cleanup rewind %s: %w", op.ID, err)
	}
	rootID := sessiontree.RootID(ctx, m.store, op.SessionID)
	m.captures.Capture.DropAnchors(ctx, checkpoint, rootID, op.CheckpointAnchorIDs)
	return nil
}

// Committed receipts remain replayable for this period; pending operations do not expire.
const rewindCommittedRetention = 24 * time.Hour

// Startup removes committed journals and anchors before expiring their receipts.
func (m *Rewinds) sweepCommittedRewinds(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	rows, err := m.store.CommittedRewindOperationsForSweep(ctx)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-rewindCommittedRetention)
	var sweepErr error
	for _, row := range rows {
		checkpoint := sessioncheckpoint.New(m.captures.dataDir, row.ProjectDir, m.store)
		if checkpoint != nil {
			if journal, loadErr := sessioncheckpoint.LoadJournal(row.JournalPath); loadErr == nil {
				sweepErr = errors.Join(sweepErr, journal.Cleanup(checkpoint))
			} else if !errors.Is(loadErr, os.ErrNotExist) {
				sweepErr = errors.Join(sweepErr, fmt.Errorf("sweep rewind %s journal: %w", row.ID, loadErr))
			}
			rootID := sessiontree.RootID(ctx, m.store, row.SessionID)
			m.captures.Capture.DropAnchors(ctx, checkpoint, rootID, row.CheckpointAnchorIDs)
		}
		if row.CreatedAt.After(cutoff) {
			continue
		}
		if err := m.store.DeleteRewindOperation(ctx, row.ID, cutoff); err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("delete swept rewind %s: %w", row.ID, err))
		}
	}
	return sweepErr
}

func (m *Rewinds) rewindRecoveryResponse(ctx context.Context, op store.RewindOperation, journal *sessioncheckpoint.Journal) (api.RewindSessionResponse, error) {
	messages, err := m.store.GetMessages(ctx, op.SessionID)
	if err != nil {
		return api.RewindSessionResponse{}, err
	}
	anchor, ok := findMessage(messages, op.AnchorMessageID)
	if !ok || !api.IsUserIntentMessage(anchor) {
		return api.RewindSessionResponse{}, ErrRewindAnchorNotFound
	}
	return api.RewindSessionResponse{
		RestoredPaths:        journal.Source.Paths(),
		RestoredPrompt:       api.MessageUserInstructionContent(anchor),
		RestoredContentParts: append([]api.MessageContentPart(nil), anchor.ContentParts...),
		RestoredArtifactIDs:  append([]string(nil), anchor.ArtifactIDs...),
	}, nil
}

// assertRewindIdle rejects a rewind that would race live work.
func (m *Rewinds) assertRewindIdle(ctx context.Context, sess *api.Session) error {
	if sess == nil {
		return store.ErrSessionNotFound
	}
	switch sess.Status {
	case api.SessionStatusIdle, api.SessionStatusError:
	default:
		return fmt.Errorf("%w: status %s", ErrSessionNotIdle, sess.Status)
	}
	state := m.runtime.WorkersInFlight(ctx, sess)
	if state > 0 {
		return fmt.Errorf("%w: %d worker(s) in flight", ErrSessionNotIdle, state)
	}
	return nil
}

func findMessage(msgs []api.Message, id string) (api.Message, bool) {
	for _, m := range msgs {
		if m.ID == id {
			return m, true
		}
	}
	return api.Message{}, false
}

// Collect unreachable checkpoint anchors while their transcript messages still exist.
func eligibleAnchorIDsFrom(msgs []api.Message, anchorMessageID string) []string {
	var ids []string
	dropping := false
	for _, msg := range msgs {
		if msg.ID == anchorMessageID {
			dropping = true
		}
		if !dropping || !api.IsUserIntentMessage(msg) {
			continue
		}
		ids = append(ids, msg.ID)
	}
	return ids
}

// Convert apply panics to errors so the recorded journal phase can guide rollback.
func applyRewindJournal(journal *sessioncheckpoint.Journal) (restored []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic applying rewind journal: %v", r)
		}
	}()
	return journal.Apply()
}

// publishRewound invalidates the transcript after rows are removed.
func (m *Rewinds) publishRewound(ctx context.Context, sessionID string) {
	if m == nil || m.events == nil || m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return
	}
	lastMessage, _ := m.store.LastTurnMessageContent(ctx, sessionID)
	m.events.PublishSession(ctx, sess.ProjectID, sessionID,
		sess.Status, lastMessage)
}
