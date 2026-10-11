package searchadmin

import (
	"time"

	"github.com/lycaon/lycaon/internal/search"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func toWireSearchResponse(result *search.Result) wire.SearchResponse {
	if result == nil {
		return wire.SearchResponse{}
	}
	out := wire.SearchResponse{
		Hits:             make([]wire.SearchHit, 0, len(result.Hits)),
		Status:           wire.SearchResultStatus(result.Status),
		Exhaustive:       result.Exhaustive,
		TotalHits:        len(result.Hits),
		CountRelation:    wire.SearchCountRelation(result.CountRelation),
		FacetsExhaustive: result.Exhaustive,
		Facets:           make([]wire.SearchFacet, 0, len(result.Facets)),
		Histogram:        make([]wire.SearchHistogramBucket, 0, len(result.Histogram)),
		Issues:           make([]wire.SearchIssue, 0, len(result.Issues)),
		Interpreted: wire.SearchInterpretation{
			Scope:                   wire.SearchScopeMode(result.Interpretation.Scope),
			Slug:                    result.Interpretation.Slug,
			FTSTerms:                append([]string(nil), result.Interpretation.FTSTerms...),
			Filters:                 make([]wire.SearchInterpretedFilter, 0, len(result.Interpretation.Filters)),
			DependencyTreesExcluded: result.Interpretation.DependencyTreesExcluded,
		},
	}
	for _, f := range result.Interpretation.Filters {
		out.Interpreted.Filters = append(out.Interpreted.Filters, wire.SearchInterpretedFilter{
			Field:   f.Field,
			Value:   f.Value,
			Negated: f.Negated,
		})
	}
	for _, hit := range result.Hits {
		var highlights []wire.SourceSearchHighlight
		for _, span := range hit.TitleHighlights {
			highlights = append(highlights, wire.SourceSearchHighlight{Start: span.Start, End: span.End})
		}
		var createdAt time.Time
		if hit.TS != "" {
			if t, err := time.Parse(time.RFC3339Nano, hit.TS); err == nil {
				createdAt = t
			}
		}
		out.Hits = append(out.Hits, wire.SearchHit{
			HitID:           hit.ID,
			HitKind:         hit.HitKind,
			Source:          hit.Source,
			Score:           hit.Score,
			SessionID:       hit.SessionID,
			SourceRef:       hit.SourceRef,
			LegID:           hit.LegID,
			Handle:          hit.Handle,
			ParentSessionID: hit.ParentSessionID,
			WorkerID:        hit.WorkerID,
			ProjectID:       hit.ProjectID,
			RootID:          hit.RootID,
			ProjectName:     hit.ProjectName,
			Snippet:         hit.Snippet,
			Title:           hit.Title,
			Context:         hit.Context,
			Path:            hit.Path,
			Line:            hit.Line,
			URL:             hit.URL,
			Trust:           hit.Trust,
			Verified:        hit.Verified,
			HintCode:        hit.HintCode,
			SymbolKind:      wire.SourceSymbolKind(hit.SymbolKind),
			TitleHighlights: highlights,
			CreatedAt:       createdAt,
		})
	}
	for _, facet := range result.Facets {
		wf := wire.SearchFacet{
			Key:    facet.Key,
			Values: make([]wire.SearchFacetValue, 0, len(facet.Values)),
		}
		for _, value := range facet.Values {
			wf.Values = append(wf.Values, wire.SearchFacetValue{
				Value: value.Value,
				Count: value.Count,
			})
		}
		out.Facets = append(out.Facets, wf)
	}
	for _, bucket := range result.Histogram {
		out.Histogram = append(out.Histogram, wire.SearchHistogramBucket{
			Key:   bucket.Key,
			Count: bucket.Count,
		})
	}
	out.Issues = searchIssuesWire(result.Issues)
	return out
}

func searchIssuesWire(issues []search.Issue) []wire.SearchIssue {
	out := make([]wire.SearchIssue, 0, len(issues))
	for _, issue := range issues {
		out = append(out, wire.SearchIssue{Executor: issue.Executor, Reason: wire.SearchIssueReason(issue.Reason),
			Limit: issue.Limit, Count: issue.Count, Message: issue.Message})
	}
	return out
}

func matchFlagsFromSearchRequest(req wire.SearchRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromSearchExport(req wire.SearchExportRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromReplacePreview(req wire.SearchReplacePreviewRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}

func matchFlagsFromReplaceApply(req wire.SearchReplaceApplyRequest) search.MatchFlags {
	return search.MatchFlags{
		Regex:         req.Regex,
		CaseSensitive: req.CaseSensitive,
		WholeWord:     req.WholeWord,
		Include:       append([]string(nil), req.Include...),
		Exclude:       append([]string(nil), req.Exclude...),
	}
}
