package sourceledger

import "context"

// Use each publication's character identities even when the comparison spans
// an older snapshot-only version or an intervening filesystem observation.
func (s *Comparisons) recordedTextAuthors(ctx context.Context, beforeID, afterID string) (*ComparisonAttribution, []Contributor, error) {
	after, err := s.versionTextState(ctx, afterID)
	if err != nil {
		return nil, nil, err
	}
	if after == nil {
		authors, err := s.savedVersionAuthors(ctx, afterID)
		if len(authors) > 1 {
			authors = nil
		}
		return nil, authors, err
	}
	before, err := s.versionTextState(ctx, beforeID)
	if err != nil {
		return nil, nil, err
	}
	selection := ContributionSelection{ThroughRevision: after.Revision, Inserted: after.Spans}
	if before != nil && before.DocumentID == after.DocumentID && before.Epoch == after.Epoch {
		selection.Inserted = changedTextSpans(after.Spans, before.Spans)
		selection.Deleted = changedTextSpans(before.Spans, after.Spans)
	}
	contributions, err := s.DocumentContributions(ctx, after.DocumentID, after.Epoch, selection)
	if err != nil {
		return nil, nil, err
	}
	labels, err := s.contributionLabels(ctx, contributions)
	if err != nil {
		return nil, nil, err
	}
	inserted, deleted := identityAuthors{}, identityAuthors{}
	for _, c := range contributions {
		if c.Revision > after.Revision {
			break
		}
		author := Contributor{Origin: c.Origin, PersonID: c.PersonID, SessionID: c.SessionID, ActorLabel: labels[c.SessionID],
			Turn: c.Turn, ToolCallID: c.ToolCallID, ToolName: c.ToolName, JobID: c.JobID}
		inserted.add(c.Inserted, author)
		deleted.add(c.Deleted, author)
	}
	result := &ComparisonAttribution{After: attributeText(after.Spans, inserted, Baseline{}, ScopeComparisonOptions{})}
	if before != nil && before.DocumentID == after.DocumentID && before.Epoch == after.Epoch {
		result.Before = attributeText(changedTextSpans(before.Spans, after.Spans), deleted, Baseline{}, ScopeComparisonOptions{})
	}
	return result, nil, nil
}

func transferRecordedAuthors(index identityAuthors, spans []TextSpan, runs []AttributedText, fallback []Contributor) {
	for _, span := range spans {
		for _, author := range fallback {
			index.add([]TextIdentityRange{{Client: span.Client, Start: span.Clock, End: span.Clock + span.Length}}, author)
		}
		for _, run := range runs {
			start, end := max(span.Index, run.Index), min(span.Index+span.Length, run.Index+run.Length)
			if start >= end {
				continue
			}
			for _, author := range run.Contributors {
				index.add([]TextIdentityRange{{Client: span.Client, Start: span.Clock + start - span.Index, End: span.Clock + end - span.Index}}, author)
			}
		}
	}
}
