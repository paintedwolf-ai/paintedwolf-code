package briefingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
)

// targetSection is a target's view of one section: resolved is false while
// the section has no stored result.
type targetSection struct {
	SectionID    string `json:"section_id"`
	ManifestHash string `json:"manifest_hash"`
	Resolved     bool   `json:"resolved"`
}

func targetSections(ctx context.Context, q *db.Queries, projectID string, hashes []briefing.SectionHash) (string, error) {
	keys := make([]string, len(hashes))
	for i, hash := range hashes {
		keys[i] = hash.ManifestHash
	}
	stored := map[string]bool{}
	for start := 0; start < len(keys); start += lookupChunk {
		rows, err := q.ListBriefingResults(ctx, db.ListBriefingResultsParams{ProjectID: projectID, Hashes: keys[start:min(start+lookupChunk, len(keys))]})
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			stored[row.ManifestHash] = true
		}
	}
	out := make([]targetSection, len(hashes))
	for i, hash := range hashes {
		out[i] = targetSection{SectionID: hash.SectionID, ManifestHash: hash.ManifestHash, Resolved: stored[hash.ManifestHash]}
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func invocationClass(request briefing.Request) string {
	if request.Manual {
		return "requested"
	}
	return "overlay"
}

func regenerateOutcome(request briefing.Request, outcome briefing.RegenerateOutcome) string {
	if !request.Manual {
		return ""
	}
	return string(outcome)
}

// StartAttempt joins the target's pending attempt or starts one. It never
// clears the record a target already shows.
func (s *Store) StartAttempt(ctx context.Context, request briefing.Request, epoch int64, hashes []briefing.SectionHash) (briefing.Attempt, error) {
	var attempt briefing.Attempt
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := checkEpoch(ctx, q, request.ProjectID, epoch); err != nil {
			return err
		}
		sections, err := targetSections(ctx, q, request.ProjectID, hashes)
		if err != nil {
			return err
		}
		current, err := q.GetBriefingTarget(ctx, db.GetBriefingTargetParams{ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey})
		now := s.stamp()
		id := s.newID()
		switch {
		case errors.Is(err, sql.ErrNoRows):
			attempt = briefing.Attempt{ID: id, Epoch: epoch, Revision: 1}
			return q.InsertBriefingTarget(ctx, db.InsertBriefingTargetParams{
				ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey,
				TargetKind: request.TargetKind, TargetJson: targetJSON(request), ScopeFingerprint: request.ScopeFingerprint,
				SectionsJson: sections, CacheEpoch: epoch, AttemptID: id, AttemptTrigger: request.Trigger(),
				InvocationClass: invocationClass(request), AttemptStatus: "pending", AttemptStartedAt: db.NullString(now),
				RegenerateOutcome: regenerateOutcome(request, briefing.Started), CreatedAt: now, UpdatedAt: now,
			})
		case err != nil:
			return err
		case current.AttemptStatus == "pending":
			attempt = briefing.Attempt{ID: current.AttemptID, Joined: true, Epoch: epoch, Revision: current.Revision}
			return nil
		}
		revision, err := q.StartBriefingTargetAttempt(ctx, db.StartBriefingTargetAttemptParams{
			TargetKind: request.TargetKind, TargetJson: targetJSON(request), ScopeFingerprint: request.ScopeFingerprint,
			SectionsJson: sections, AttemptID: id, AttemptTrigger: request.Trigger(), InvocationClass: invocationClass(request),
			AttemptStartedAt: db.NullString(now), RegenerateOutcome: regenerateOutcome(request, briefing.Started), UpdatedAt: now,
			ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey, CacheEpoch: epoch,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return briefing.ErrStaleEpoch
		}
		attempt = briefing.Attempt{ID: id, Epoch: epoch, Revision: revision}
		return err
	})
	return attempt, err
}

func targetJSON(request briefing.Request) string {
	if request.TargetJSON == "" {
		return "{}"
	}
	return request.TargetJSON
}

// CompleteAttempt applies only for the target's active attempt in the
// current epoch, then releases the record the target showed before.
func (s *Store) CompleteAttempt(ctx context.Context, request briefing.Request, attemptID string, hashes []briefing.SectionHash, recordID string) (briefing.Settled, error) {
	var settled briefing.Settled
	err := s.inTx(ctx, func(q *db.Queries) error {
		prior, err := q.GetBriefingTarget(ctx, db.GetBriefingTargetParams{ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		sections, err := targetSections(ctx, q, request.ProjectID, hashes)
		if err != nil {
			return err
		}
		revision, err := q.CompleteBriefingTargetAttempt(ctx, db.CompleteBriefingTargetAttemptParams{
			SectionsJson: sections, RecordID: db.NullString(recordID), UpdatedAt: s.stamp(),
			ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey,
			AttemptID: attemptID, CacheEpoch: prior.CacheEpoch,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		settled = briefing.Settled{Applied: true, Revision: revision}
		return releaseReplaced(ctx, q, request.ProjectID, prior.RecordID, recordID)
	})
	return settled, err
}

func (s *Store) FailAttempt(ctx context.Context, request briefing.Request, attemptID string, failure briefing.Failure) (briefing.Settled, error) {
	var settled briefing.Settled
	err := s.inTx(ctx, func(q *db.Queries) error {
		revision, err := q.FailBriefingTargetAttempt(ctx, db.FailBriefingTargetAttemptParams{
			ErrorCode: failure.Code, ErrorMessage: failure.Message, UpdatedAt: s.stamp(),
			ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey, AttemptID: attemptID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		settled = briefing.Settled{Applied: err == nil, Revision: revision}
		return err
	})
	return settled, err
}

// Resolve points a target at a record built entirely from stored results.
// An unchanged target keeps its revision.
func (s *Store) Resolve(ctx context.Context, request briefing.Request, epoch int64, hashes []briefing.SectionHash, recordID string) (briefing.Settled, error) {
	var settled briefing.Settled
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := checkEpoch(ctx, q, request.ProjectID, epoch); err != nil {
			return err
		}
		sections, err := targetSections(ctx, q, request.ProjectID, hashes)
		if err != nil {
			return err
		}
		outcome := regenerateOutcome(request, briefing.InputsUnchanged)
		prior, err := q.GetBriefingTarget(ctx, db.GetBriefingTargetParams{ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey})
		if errors.Is(err, sql.ErrNoRows) {
			now := s.stamp()
			settled = briefing.Settled{Applied: true, Revision: 1}
			return q.InsertBriefingTarget(ctx, db.InsertBriefingTargetParams{
				ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey,
				TargetKind: request.TargetKind, TargetJson: targetJSON(request), ScopeFingerprint: request.ScopeFingerprint,
				SectionsJson: sections, RecordID: db.NullString(recordID), CacheEpoch: epoch, AttemptStatus: "idle",
				RegenerateOutcome: outcome, CreatedAt: now, UpdatedAt: now,
			})
		}
		if err != nil {
			return err
		}
		revision, err := q.ResolveBriefingTarget(ctx, db.ResolveBriefingTargetParams{
			SectionsJson: sections, RecordID: db.NullString(recordID), RegenerateOutcome: outcome, UpdatedAt: s.stamp(),
			ProjectID: request.ProjectID, ProfileID: request.ProfileID, TargetKey: request.TargetKey, CacheEpoch: epoch,
		})
		if errors.Is(err, sql.ErrNoRows) {
			settled = briefing.Settled{Applied: prior.CacheEpoch == epoch, Revision: prior.Revision}
			return nil
		}
		if err != nil {
			return err
		}
		settled = briefing.Settled{Applied: true, Revision: revision}
		return releaseReplaced(ctx, q, request.ProjectID, prior.RecordID, recordID)
	})
	return settled, err
}

// releaseReplaced deletes the record a target stopped showing, and its
// manifests, when nothing else holds them.
func releaseReplaced(ctx context.Context, q *db.Queries, projectID string, prior sql.NullString, next string) error {
	if !prior.Valid || prior.String == next {
		return nil
	}
	return releaseRecord(ctx, q, projectID, prior.String)
}

func releaseRecord(ctx context.Context, q *db.Queries, projectID, recordID string) error {
	sections, err := q.ListBriefingRecordSections(ctx, db.ListBriefingRecordSectionsParams{ProjectID: projectID, RecordID: recordID})
	if err != nil {
		return err
	}
	deleted, err := q.DeleteBriefingRecordIfUnreferenced(ctx, db.DeleteBriefingRecordIfUnreferencedParams{ProjectID: projectID, RecordID: recordID})
	if err != nil || deleted == 0 {
		return err
	}
	for _, section := range sections {
		if _, err := q.DeleteBriefingManifestIfUnreferenced(ctx, db.DeleteBriefingManifestIfUnreferencedParams{ProjectID: projectID, ManifestHash: section.ManifestHash}); err != nil {
			return err
		}
	}
	return nil
}

// FailInterrupted fails every pending attempt. It runs before the engine
// accepts work, when no attempt can still be running.
func (s *Store) FailInterrupted(ctx context.Context) (int64, error) {
	var failed int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		failed, err = q.FailInterruptedBriefingAttempts(ctx, s.stamp())
		return err
	})
	return failed, err
}

// Intent is the persisted exact action an approval was asked about.
type Intent struct {
	Text         string
	CheckpointID string
	ActionID     string
	Scope        string
}

// LinkApprovalIntent attaches an approval's persisted action to its target
// without touching sections, record or attempt.
func (s *Store) LinkApprovalIntent(ctx context.Context, projectID, profileID, targetKey string, intent Intent) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		_, err := q.LinkBriefingApprovalIntent(ctx, db.LinkBriefingApprovalIntentParams{
			IntentText: db.NullString(intent.Text), IntentCheckpointID: db.NullString(intent.CheckpointID),
			IntentActionID: db.NullString(intent.ActionID), IntentScope: db.NullString(intent.Scope),
			UpdatedAt: s.stamp(), ProjectID: projectID, ProfileID: profileID, TargetKey: targetKey,
		})
		return err
	})
}

// PinCheckpoint keeps a record and its manifests for as long as the
// checkpoint exists.
func (s *Store) PinCheckpoint(ctx context.Context, projectID, checkpointID, recordID string) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		sections, err := q.ListBriefingRecordSections(ctx, db.ListBriefingRecordSectionsParams{ProjectID: projectID, RecordID: recordID})
		if err != nil {
			return err
		}
		if len(sections) == 0 {
			return sql.ErrNoRows
		}
		for _, section := range sections {
			if err := q.InsertBriefingCheckpointPin(ctx, db.InsertBriefingCheckpointPinParams{
				CheckpointID: checkpointID, ManifestHash: section.ManifestHash, ProjectID: projectID, RecordID: recordID, CreatedAt: s.stamp(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
