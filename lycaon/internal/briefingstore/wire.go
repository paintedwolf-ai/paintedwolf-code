package briefingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/db"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// SnapshotQuery names the target a snapshot describes.
type SnapshotQuery struct {
	ProjectID string
	ProfileID string
	Version   string
	Policy    briefing.Policy
	TargetKey string
}

// Snapshot renders a target's stored state; a target never requested is
// absent at the current epoch.
func (s *Store) Snapshot(ctx context.Context, query SnapshotQuery) (wire.BriefingSnapshot, error) {
	out := wire.BriefingSnapshot{ProfileID: query.ProfileID, ProfileVersion: query.Version, TargetKey: query.TargetKey,
		Target: map[string]any{}, Status: "absent", Revision: "0", PendingSections: []string{}, Outcome: wire.BriefingOutcome{State: "unknown"}}
	target, err := s.GetTarget(ctx, query.ProjectID, query.ProfileID, query.TargetKey)
	if errors.Is(err, sql.ErrNoRows) {
		epoch, err := s.Epoch(ctx, query.ProjectID)
		out.CacheEpoch = strconv.FormatInt(epoch, 10)
		return out, err
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(target.TargetJSON), &out.Target); err != nil {
		return out, err
	}
	out.ScopeFingerprint = target.ScopeFingerprint
	out.CacheEpoch, out.Revision = strconv.FormatInt(target.Epoch, 10), strconv.FormatInt(target.Revision, 10)
	out.Status, out.AttemptID, out.AttemptTrigger, out.InvocationClass = target.Attempt.Status, target.Attempt.ID, target.Attempt.Trigger, target.Attempt.Class
	out.RegenerateOutcome, out.ErrorCode, out.ErrorMessage = string(target.Attempt.Regenerate), target.Attempt.ErrorCode, target.Attempt.Error
	out.UpdatedAt = db.FormatTime(target.UpdatedAt)
	if target.Attempt.Status == "pending" {
		out.ReconcileAfter = db.FormatTime(query.Policy.Deadline(target.Attempt.StartedAt))
		for _, section := range target.Sections {
			if !section.Resolved {
				out.PendingSections = append(out.PendingSections, section.SectionID)
			}
		}
	}
	if intent := target.Facts.Intent; intent != nil {
		out.LinkedIntent = &wire.BriefingLinkedIntent{Text: intent.Text, CheckpointID: intent.CheckpointID, ActionID: intent.ActionID, Scope: intent.Scope}
	}
	outcome := target.Facts.Outcome
	out.Outcome = wire.BriefingOutcome{State: outcome.State, ReceiptID: outcome.ReceiptID, ExitCode: outcome.ExitCode}
	if target.RecordID != "" {
		result, err := s.result(ctx, query.ProjectID, target)
		if err != nil {
			return out, err
		}
		out.Result = result
	}
	return out, nil
}

func (s *Store) result(ctx context.Context, projectID string, target Target) (*wire.BriefingResult, error) {
	record, err := s.GetRecord(ctx, projectID, target.RecordID)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(record.Sections))
	for i, section := range record.Sections {
		hashes[i] = section.ManifestHash
	}
	results, err := s.GetResults(ctx, projectID, hashes)
	if err != nil {
		return nil, err
	}
	out := &wire.BriefingResult{EvidenceRef: wire.BriefingEvidenceRef{ProjectID: projectID, RecordID: record.ID}}
	for _, section := range record.Sections {
		result, ok := results[section.ManifestHash]
		if !ok {
			continue
		}
		reused := target.Attempt.StartedAt.IsZero() || result.CreatedAt.Before(target.Attempt.StartedAt)
		out.Summary = append(out.Summary, summarySection(section, result, reused))
	}
	return out, nil
}

func summarySection(section briefing.SectionHash, result briefing.Result, reused bool) wire.BriefingSummarySection {
	return wire.BriefingSummarySection{
		SectionID: section.SectionID, Text: result.Text, ManifestHash: section.ManifestHash, Source: string(result.Source),
		Reused: reused, ProviderID: result.Provider, Model: result.Model, EvidenceQuality: append([]string{}, result.Quality...),
		CreatedAt: db.FormatTime(result.CreatedAt),
	}
}

// Readable reports whether the current reader may see items scoped to a
// session; an empty audience is readable by anyone who can read the project.
type Readable func(ctx context.Context, audience string) (bool, error)

type displayManifest struct {
	Template struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"template"`
	Items []struct {
		wire.BriefingRecordItem
		Audience string `json:"audience"`
	} `json:"items"`
}

// Record renders an evidence record for one reader. A section whose items
// the reader cannot all read carries neither text nor items.
func (s *Store) Record(ctx context.Context, projectID, recordID string, readable Readable) (wire.BriefingRecord, error) {
	record, err := s.GetRecord(ctx, projectID, recordID)
	if err != nil {
		return wire.BriefingRecord{}, err
	}
	out := wire.BriefingRecord{RecordID: record.ID, ProfileID: record.ProfileID, ProfileVersion: record.Version, Method: record.Method,
		AttemptTrigger: record.Origin.Trigger, CreatedAt: db.FormatTime(record.CreatedAt), Sections: []wire.BriefingRecordSection{}}
	for _, section := range record.Sections {
		rendered, err := s.recordSection(ctx, projectID, section, readable)
		if err != nil {
			return wire.BriefingRecord{}, err
		}
		out.Sections = append(out.Sections, rendered)
	}
	return out, nil
}

func (s *Store) recordSection(ctx context.Context, projectID string, section briefing.SectionHash, readable Readable) (wire.BriefingRecordSection, error) {
	out := wire.BriefingRecordSection{SectionID: section.SectionID, ManifestHash: section.ManifestHash}
	raw, err := s.ReadManifest(ctx, projectID, section.ManifestHash)
	if err != nil {
		return out, err
	}
	var manifest displayManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return out, err
	}
	items := make([]wire.BriefingRecordItem, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		ok, err := readable(ctx, item.Audience)
		if err != nil || !ok {
			return out, err
		}
		items = append(items, item.BriefingRecordItem)
	}
	results, err := s.GetResults(ctx, projectID, []string{section.ManifestHash})
	if err != nil {
		return out, err
	}
	if result, ok := results[section.ManifestHash]; ok {
		summary := summarySection(section, result, false)
		out.Result = &summary
	}
	out.Available, out.Items = true, items
	out.TemplateID, out.TemplateVersion = manifest.Template.ID, manifest.Template.Version
	return out, nil
}
