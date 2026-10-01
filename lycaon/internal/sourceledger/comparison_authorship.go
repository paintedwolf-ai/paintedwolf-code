package sourceledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/lycaon/lycaon/pkg/api"
)

// AttributedText identifies an actual changed run, in UTF-16 coordinates.
type AttributedText struct {
	Index, Length     uint32
	Contributors      []Contributor
	Selected, Visible bool
}

type ComparisonAttribution struct{ Before, After []AttributedText }

func (s *Store) versionTextState(ctx context.Context, versionID string) (*TextState, error) {
	row, err := s.queries.GetSourceVersionTextState(ctx, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state := &TextState{DocumentID: row.DocumentID, Epoch: row.Epoch, Revision: row.Revision}
	if err := json.Unmarshal([]byte(row.SpansJson), &state.Spans); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Store) attachComparisonAttribution(ctx context.Context, baseline Baseline, opts ScopeComparisonOptions, out *Comparison) error {
	before, err := s.versionTextState(ctx, out.Before.VersionID)
	if err != nil {
		return err
	}
	after, err := s.versionTextState(ctx, out.After.VersionID)
	if err != nil {
		return err
	}
	if before == nil || after == nil || before.DocumentID != after.DocumentID || before.Epoch != after.Epoch {
		return s.attachRecordedTextAttribution(ctx, baseline, opts, out)
	}
	contributions, err := s.DocumentContributions(ctx, after.DocumentID, after.Epoch, ContributionSelection{ThroughRevision: after.Revision, Inserted: changedTextSpans(after.Spans, before.Spans), Deleted: changedTextSpans(before.Spans, after.Spans)})
	if err != nil {
		return err
	}
	labels, err := s.contributionLabels(ctx, contributions)
	if err != nil {
		return err
	}
	insertions, deletions := identityAuthors{}, identityAuthors{}
	for _, c := range contributions {
		if c.Revision > after.Revision {
			break
		}
		author := Contributor{Origin: c.Origin, SessionID: c.SessionID, Turn: c.Turn,
			PersonID: c.PersonID, ActorLabel: labels[c.SessionID], ToolCallID: c.ToolCallID, ToolName: c.ToolName, JobID: c.JobID}
		insertions.add(c.Inserted, author)
		deletions.add(c.Deleted, author)
	}
	out.Attribution = &ComparisonAttribution{
		Before: attributeText(changedTextSpans(before.Spans, after.Spans), deletions, baseline, opts),
		After:  attributeText(changedTextSpans(after.Spans, before.Spans), insertions, baseline, opts),
	}

	return nil
}

func (s *Store) contributionLabels(ctx context.Context, contributions []TextContribution) (map[string]string, error) {
	ids := make(map[string]bool)
	for _, c := range contributions {
		if c.SessionID != "" {
			ids[c.SessionID] = true
		}
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	raw, err := jsonArray(keys)
	if err != nil {
		return nil, err
	}
	rows, err := s.sqlDB.QueryContext(ctx, `SELECT id,COALESCE(title,'') FROM sessions WHERE id IN (SELECT value FROM json_each(?))`, raw)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	labels := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		labels[id] = title
	}
	return labels, rows.Err()
}

type identityAuthor struct {
	start, end uint32
	author     Contributor
}
type identityAuthors map[uint32][]identityAuthor

func (index identityAuthors) add(ranges []TextIdentityRange, author Contributor) {
	for _, r := range ranges {
		index[r.Client] = append(index[r.Client], identityAuthor{r.Start, r.End, author})
	}
}

// Split at authorship boundaries; concurrent deletions may name several authors.
func attributeText(spans []TextSpan, authors identityAuthors, baseline Baseline, opts ScopeComparisonOptions) []AttributedText {
	out := make([]AttributedText, 0, len(spans))
	index := indexIdentityAuthors(authors)
	for _, span := range spans {
		end := span.Clock + span.Length
		boundaries := []uint32{span.Clock, end}
		var overlaps []identityAuthor
		entries := index[span.Client]
		first := sort.Search(len(entries), func(i int) bool { return entries[i].through > span.Clock })
		for _, indexed := range entries[first:] {
			a := indexed.identityAuthor
			if a.start >= end {
				break
			}
			if a.end <= span.Clock {
				continue
			}
			overlaps = append(overlaps, a)
			boundaries = append(boundaries, max(span.Clock, a.start), min(end, a.end))
		}
		sort.Slice(boundaries, func(i, j int) bool { return boundaries[i] < boundaries[j] })
		for i := 1; i < len(boundaries); i++ {
			start, stop := boundaries[i-1], boundaries[i]
			if start == stop {
				continue
			}
			run := AttributedText{Index: span.Index + start - span.Clock, Length: stop - start, Contributors: []Contributor{}}
			seen := map[Contributor]bool{}
			for _, a := range overlaps {
				if a.start > start || a.end < stop || seen[a.author] {
					continue
				}
				seen[a.author] = true
				run.Contributors = append(run.Contributors, a.author)
				visible := !opts.UnmarkUserEdits || a.author.Origin != api.SourceChangeOriginUser
				run.Visible = run.Visible || visible
				run.Selected = run.Selected || (visible && contributorInBaseline(a.author, baseline))
			}
			if len(run.Contributors) == 0 {
				run.Visible = true
				run.Selected = baseline.Kind != BaselineSession && baseline.Kind != BaselineTurn
			}
			out = append(out, run)
		}
	}
	return out
}

func contributorInBaseline(author Contributor, baseline Baseline) bool {
	switch baseline.Kind {
	case BaselineSession:
		return author.SessionID == baseline.SessionID
	case BaselineTurn:
		return author.SessionID == baseline.SessionID && author.Turn == baseline.Turn
	default:
		return true
	}
}

type indexedIdentityAuthor struct {
	identityAuthor
	through uint32
}

func indexIdentityAuthors(authors identityAuthors) map[uint32][]indexedIdentityAuthor {
	index := make(map[uint32][]indexedIdentityAuthor, len(authors))
	for client, entries := range authors {
		sort.Slice(entries, func(i, j int) bool { return entries[i].start < entries[j].start })
		through := uint32(0)
		for _, entry := range entries {
			through = max(through, entry.end)
			index[client] = append(index[client], indexedIdentityAuthor{entry, through})
		}
	}
	return index
}
