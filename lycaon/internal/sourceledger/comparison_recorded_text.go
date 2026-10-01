package sourceledger

import (
	"context"
	"errors"
	"math"

	"github.com/lycaon/lycaon/pkg/api"
)

// Recorded file writes have snapshot authorship. Replaying their actual text
// deltas retains unchanged ranges without inventing a comparison endpoint.
func (s *Store) attachRecordedTextAttribution(ctx context.Context, baseline Baseline, opts ScopeComparisonOptions, out *Comparison) error {
	if !textSide(out.Before) || !textSide(out.After) {
		return nil
	}
	ids, err := s.comparisonVersionChain(ctx, out.Before.VersionID, out.After.VersionID)
	if err != nil {
		return err
	}
	if ids == nil {
		return nil
	}
	length, err := attributedTextLength(attributionText(out.Before.Content))
	if err != nil {
		return err
	}
	initial := []TextSpan{{Length: length}}
	spans := initial
	text := attributionText(out.Before.Content)
	inserted, deleted := identityAuthors{}, identityAuthors{}
	var client uint32
	for i := len(ids) - 1; i >= 0; i-- {
		version, err := s.queries.GetSourceVersion(ctx, ids[i])
		if err != nil {
			return err
		}
		side, err := s.comparisonSide(ctx, version.ProjectID, version.ID)
		if err != nil {
			return err
		}
		if !textSide(side) {
			return nil
		}
		attribution, authors, err := s.recordedTextAuthors(ctx, version.ParentVersionID, version.ID)
		if err != nil {
			return err
		}
		if client == math.MaxUint32 {
			return errors.New("source comparison exceeds text identity capacity")
		}
		client++
		var additions, removals []AttributedText
		if attribution != nil {
			additions, removals = attribution.After, attribution.Before
		}
		var clock uint32
		edits, err := s.recordedTextEdits(ctx, version.ParentVersionID, version.ID, text, attributionText(side.Content))
		if err != nil {
			return err
		}
		for _, edit := range edits {
			removed := sliceTextSpans(spans, edit.Index, edit.Index+edit.Delete)
			length, err := attributedTextLength(edit.Insert)
			if err != nil {
				return err
			}
			if length > math.MaxUint32-clock {
				return errors.New("source comparison exceeds text identity capacity")
			}
			added := TextSpan{Client: client, Clock: clock, Length: length}
			transferRecordedAuthors(deleted, removed, removals, authors)
			clock += added.Length
			before := sliceTextSpans(spans, 0, edit.Index)
			after := sliceTextSpans(spans, edit.Index+edit.Delete, ^uint32(0))
			before = append(before, added)
			before = append(before, after...)
			spans = before
			var index uint32
			for j := range spans {
				spans[j].Index = index
				index += spans[j].Length
			}
		}
		for _, span := range spans {
			if span.Client == client {
				transferRecordedAuthors(inserted, []TextSpan{span}, additions, authors)
			}
		}
		text = attributionText(side.Content)

	}
	out.Attribution = &ComparisonAttribution{
		Before: attributeText(changedTextSpans(initial, spans), deleted, baseline, opts),
		After:  attributeText(changedTextSpans(spans, initial), inserted, baseline, opts),
	}
	return nil
}

func textSide(side ComparisonSide) bool {
	return side.Availability == ContentAvailable || side.Availability == ContentAbsent
}

func sliceTextSpans(spans []TextSpan, from, through uint32) []TextSpan {
	result := make([]TextSpan, 0)
	for _, span := range spans {
		start, end := max(from, span.Index), min(through, span.Index+span.Length)
		if start >= end {
			continue
		}
		result = append(result, TextSpan{Index: start, Length: end - start, Client: span.Client, Clock: span.Clock + start - span.Index})
	}
	return result
}

func (s *Store) comparisonVersionChain(ctx context.Context, before, after string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for after != before {
		if after == "" {
			return nil, nil
		}
		if seen[after] {
			return nil, errors.New("cyclic source version history")
		}
		seen[after] = true
		version, err := s.queries.GetSourceVersion(ctx, after)
		if err != nil {
			return nil, err
		}
		ids = append(ids, after)
		after = version.ParentVersionID
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func (s *Store) savedVersionAuthors(ctx context.Context, id string) ([]Contributor, error) {
	rows, err := s.sqlDB.QueryContext(ctx, `SELECT DISTINCT a.origin,a.session_id,a.turn,a.person_id,a.actor_label,a.tool_call_id,a.tool_name,a.job_id
 FROM source_effects e JOIN source_effect_authors a ON a.effect_id=e.id WHERE e.after_version_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	authors := make([]Contributor, 0)
	for rows.Next() {
		var author Contributor
		var origin string
		if err := rows.Scan(&origin, &author.SessionID, &author.Turn, &author.PersonID, &author.ActorLabel, &author.ToolCallID, &author.ToolName, &author.JobID); err != nil {
			return nil, err
		}
		author.Origin = api.SourceChangeOrigin(origin)
		authors = append(authors, author)
	}
	return authors, rows.Err()
}
