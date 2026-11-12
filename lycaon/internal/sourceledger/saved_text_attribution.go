package sourceledger

import (
	"context"
	"sort"
	"unicode/utf16"
)

// contributionEffects names the effect that first published each
// contribution, so a line's author can be traced to its change.
func (s *Comparisons) contributionEffects(ctx context.Context, contributions []TextContribution) (map[string]string, error) {
	out := make(map[string]string, len(contributions))
	ids := make([]string, 0, len(contributions))
	for _, c := range contributions {
		ids = append(ids, c.ID)
	}
	for start := 0; start < len(ids); start += effectLookupChunk {
		rows, err := s.queries.ListEffectsForContributions(ctx, ids[start:min(start+effectLookupChunk, len(ids))])
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, seen := out[row.ContributionID]; !seen {
				out[row.ContributionID] = row.EffectID
			}
		}
	}
	return out, nil
}

const effectLookupChunk = 500

// A saved version can contain several authors on the same line. Character
// identities retain that authorship independently of who published the file.
func (s *Comparisons) savedTextAttribution(ctx context.Context, projectID, versionID string, fallback AttributionResult) (AttributionResult, error) {
	state, err := s.versionTextState(ctx, versionID)
	if err != nil || state == nil {
		return fallback, err
	}
	side, err := s.comparisonSide(ctx, projectID, versionID)
	if err != nil || side.Availability != ContentAvailable {
		return fallback, err
	}
	contributions, err := s.DocumentContributions(ctx, state.DocumentID, state.Epoch, ContributionSelection{ThroughRevision: state.Revision, Inserted: state.Spans})
	if err != nil {
		return fallback, err
	}
	labels, err := s.contributionLabels(ctx, contributions)
	if err != nil {
		return fallback, err
	}
	effects, err := s.contributionEffects(ctx, contributions)
	if err != nil {
		return fallback, err
	}
	authors := identityAuthors{}
	metadata := map[Contributor]AttributionInterval{}
	for _, c := range contributions {
		if c.Revision > state.Revision {
			break
		}
		author := Contributor{Origin: c.Origin, PersonID: c.PersonID, SessionID: c.SessionID, Turn: c.Turn,
			ToolCallID: c.ToolCallID, ToolName: c.ToolName, ActorLabel: labels[c.SessionID]}
		authors.add(c.Inserted, author)
		metadata[author] = AttributionInterval{Origin: c.Origin, ActorLabel: author.ActorLabel, PersonID: c.PersonID,
			SessionID: c.SessionID, Turn: c.Turn, ToolCallID: c.ToolCallID, TS: c.CreatedAt, EffectID: effects[c.ID]}
	}
	text := utf16.Encode([]rune(attributionText(side.Content)))
	starts := []int{0}
	for i, ch := range text {
		if ch == '\n' {
			starts = append(starts, i+1)
		}
	}
	lineAt := func(index uint32) int { return sort.SearchInts(starts, int(index)+1) }
	result := fallback
	result.Intervals = nil
	// Runs arrive in text order; one author's runs on shared lines merge.
	latest := map[Contributor]int{}
	for _, run := range attributeText(state.Spans, authors, Baseline{}, ScopeComparisonOptions{}) {
		if run.Length == 0 {
			continue
		}
		start, end := lineAt(run.Index), lineAt(run.Index+run.Length-1)
		for _, author := range run.Contributors {
			if i, ok := latest[author]; ok && start <= result.Intervals[i].EndLine {
				result.Intervals[i].EndLine = max(result.Intervals[i].EndLine, end)
				continue
			}
			latest[author] = len(result.Intervals)
			interval := metadata[author]
			interval.StartLine, interval.EndLine = start, end
			result.Intervals = append(result.Intervals, interval)
		}
	}
	return result, nil
}
