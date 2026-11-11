package briefingstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
)

// Target is a target's stored state.
type Target struct {
	ProfileID        string
	TargetKey        string
	TargetKind       string
	TargetJSON       string
	ScopeFingerprint string
	Sections         []TargetSection
	RecordID         string
	Epoch            int64
	Revision         int64
	Attempt          AttemptView
	Intent           *Intent
	UpdatedAt        time.Time
}

// TargetSection is one section of a target's current resolution.
type TargetSection struct {
	briefing.SectionHash
	Resolved bool
}

// AttemptView is a target's latest attempt.
type AttemptView struct {
	ID         string
	Trigger    string
	Class      string
	Status     string
	StartedAt  time.Time
	ErrorCode  string
	Error      string
	Regenerate briefing.RegenerateOutcome
}

func (s *Targets) GetTarget(ctx context.Context, projectID, profileID, targetKey string) (Target, error) {
	row, err := s.queries.GetBriefingTarget(ctx, db.GetBriefingTargetParams{ProjectID: projectID, ProfileID: profileID, TargetKey: targetKey})
	if err != nil {
		return Target{}, err
	}
	return targetFromRow(row)
}

// ListTargets returns the stored targets among keys; absent keys are omitted.
func (s *Targets) ListTargets(ctx context.Context, projectID, profileID string, keys []string) ([]Target, error) {
	var out []Target
	for start := 0; start < len(keys); start += lookupChunk {
		rows, err := s.queries.ListBriefingTargets(ctx, db.ListBriefingTargetsParams{ProjectID: projectID, ProfileID: profileID, TargetKeys: keys[start:min(start+lookupChunk, len(keys))]})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			target, err := targetFromRow(row)
			if err != nil {
				return nil, err
			}
			out = append(out, target)
		}
	}
	return out, nil
}

func targetFromRow(row db.BriefingTargets) (Target, error) {
	target := Target{
		ProfileID: row.ProfileID, TargetKey: row.TargetKey, TargetKind: row.TargetKind, TargetJSON: row.TargetJson,
		ScopeFingerprint: row.ScopeFingerprint, RecordID: row.RecordID.String, Epoch: row.CacheEpoch, Revision: row.Revision,
		Attempt: AttemptView{ID: row.AttemptID, Trigger: row.AttemptTrigger, Class: row.InvocationClass, Status: row.AttemptStatus,
			ErrorCode: row.ErrorCode, Error: row.ErrorMessage, Regenerate: briefing.RegenerateOutcome(row.RegenerateOutcome)},
	}
	var sections []targetSection
	if err := json.Unmarshal([]byte(row.SectionsJson), &sections); err != nil {
		return Target{}, err
	}
	for _, section := range sections {
		target.Sections = append(target.Sections, TargetSection{SectionHash: briefing.SectionHash{SectionID: section.SectionID, ManifestHash: section.ManifestHash}, Resolved: section.Resolved})
	}
	if row.IntentText.Valid {
		target.Intent = &Intent{Text: row.IntentText.String, CheckpointID: row.IntentCheckpointID.String, ActionID: row.IntentActionID.String, Scope: row.IntentScope.String}
	}
	var err error
	if row.AttemptStartedAt.Valid {
		if target.Attempt.StartedAt, err = db.ParseTime(row.AttemptStartedAt.String); err != nil {
			return Target{}, err
		}
	}
	target.UpdatedAt, err = db.ParseTime(row.UpdatedAt)
	return target, err
}
