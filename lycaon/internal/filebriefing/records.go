package filebriefing

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/briefing"
)

// Records keeps file summaries as briefing records, so their evidence can be
// shown without another model call.
type Records interface {
	Epoch(ctx context.Context, projectID string) (int64, error)
	PutResult(ctx context.Context, projectID string, epoch int64, manifest briefing.Manifest, hash string, result briefing.Result) error
	PutRecord(ctx context.Context, projectID string, epoch int64, record briefing.Record) error
	Sweep(ctx context.Context) error
}

// recordSummary stores the generated text against its manifest and returns
// the record id, or empty when the summary cannot be recorded.
func (s *Service) recordSummary(ctx context.Context, b Briefing, manifest briefing.Manifest, text string, truncated bool) string {
	if s.records == nil || len(manifest.Items) == 0 {
		return ""
	}
	recordID, err := s.putSummary(ctx, b, manifest, text, truncated)
	if err != nil {
		s.logger.WarnContext(ctx, "record file briefing", "project_id", b.ProjectID, "path", b.Path, "error", err)
		return ""
	}
	return recordID
}

func (s *Service) putSummary(ctx context.Context, b Briefing, manifest briefing.Manifest, text string, truncated bool) (string, error) {
	hash, err := manifest.Hash()
	if err != nil {
		return "", err
	}
	epoch, err := s.records.Epoch(ctx, b.ProjectID)
	if err != nil {
		return "", err
	}
	result := briefing.Result{Text: text, Source: briefing.SourceSummarizer, CreatedAt: time.Now().UTC()}
	if truncated {
		result.Quality = []string{"output_truncated"}
	}
	if err := s.records.PutResult(ctx, b.ProjectID, epoch, manifest, hash, result); err != nil {
		return "", err
	}
	sections := []briefing.SectionHash{{SectionID: sectionID, ManifestHash: hash}}
	record := briefing.Record{
		ID: briefing.RecordID(ProfileID, profileVersion, sections), ProfileID: ProfileID, Version: profileVersion,
		Sections: sections, Origin: briefing.RecordOrigin{AttemptID: b.AttemptID, Trigger: b.Trigger},
	}
	return record.ID, s.records.PutRecord(ctx, b.ProjectID, epoch, record)
}
