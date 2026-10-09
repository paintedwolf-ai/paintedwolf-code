package sourceapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/projectsource"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Analysis) sourceIndexSnapshot(w http.ResponseWriter, r *http.Request) (projectsource.SourceIndexSnapshot, string, bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return projectsource.SourceIndexSnapshot{}, "", false
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return projectsource.SourceIndexSnapshot{}, "", false
	}
	if rootID := strings.TrimSpace(r.URL.Query().Get("root_id")); rootID != "" {
		scoped := *p
		scoped.Roots = nil
		for _, root := range p.Roots {
			if root.ID == rootID {
				scoped.Roots = append(scoped.Roots, root)
			}
		}
		if len(scoped.Roots) == 0 {
			s.Workspace.writeSourceReadError(w, r, projectsource.ErrSourceNoRoot)
			return projectsource.SourceIndexSnapshot{}, "", false
		}
		p = &scoped
	}
	return s.sourceIndexes.Snapshot(r.Context(), p), p.ID, true
}

func sourceIndexCoverage(snapshot projectsource.SourceIndexSnapshot) []wire.SourceIndexRootCoverage {
	out := make([]wire.SourceIndexRootCoverage, 0, len(snapshot.Coverage))
	for _, root := range snapshot.Coverage {
		out = append(out, wire.SourceIndexRootCoverage{RootID: root.RootID, State: sourceIndexWireState(root.State),
			DiscoveryComplete: root.DiscoveryComplete, Refreshing: root.Refreshing, BoundedDirectories: root.BoundedDirectories,
			FailedDirectories: root.FailedDirectories, Error: root.Error})
	}
	return out
}

func sourceIndexWireState(state projectsource.SourceIndexState) wire.SourceIndexState {
	switch state {
	case projectsource.SourceIndexReady:
		return wire.SourceIndexStateReady
	case projectsource.SourceIndexFailed:
		return wire.SourceIndexStateFailed
	default:
		return wire.SourceIndexStateWarming
	}
}

func (s *Analysis) HandleProjectSourceIndex(w http.ResponseWriter, r *http.Request) {
	snapshot, projectID, ok := s.sourceIndexSnapshot(w, r)
	if !ok {
		return
	}
	defer snapshot.Close()
	roots := make([]wire.SourceIndexRootSummary, 0, len(snapshot.Roots))
	for _, root := range snapshot.Roots {
		resources := make([]wire.SourceIndexResource, 0, len(root.Resources))
		for _, resource := range root.Resources {
			resources = append(resources, wire.SourceIndexResource{Kind: resource.Kind, Path: resource.Path})
		}
		roots = append(roots, wire.SourceIndexRootSummary{
			RootID: root.RootID, FileCount: root.FileCount, Resources: resources,
		})
	}
	status := http.StatusOK
	retryAfter := 0
	warmupKey := "source-index:" + projectID
	if snapshot.Refreshing {
		if snapshot.State == projectsource.SourceIndexWarming {
			status = http.StatusAccepted
		}
		retryAfter = s.warmupPolls.warming(warmupKey)
	} else {
		s.warmupPolls.ready(warmupKey)
	}
	httpio.WriteJSON(w, status, wire.SourceIndexSummary{
		State: sourceIndexWireState(snapshot.State), Revision: snapshot.Revision,
		Refreshing: snapshot.Refreshing, RetryAfterMs: retryAfter, Coverage: sourceIndexCoverage(snapshot),
		FileCount: snapshot.FileCount, Roots: roots,
	})
}

func (s *Analysis) HandleSearchProjectSource(w http.ResponseWriter, r *http.Request) {
	snapshot, projectID, ok := s.sourceIndexSnapshot(w, r)
	if !ok {
		return
	}
	defer snapshot.Close()
	pq, err := httpio.ReadPageQuery(r, sourceSearchLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	limit := pq.Limit
	query := r.URL.Query().Get("q")
	rootID := strings.TrimSpace(r.URL.Query().Get("root_id"))
	scope := sourceSearchScope(chi.URLParam(r, "id"), rootID, strings.TrimSpace(query))
	position, err := decodeSourceSearchCursor(pq.Cursor, scope, snapshot.Revision)
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	style := projectsource.HostSourcePathStyle()
	parsed := projectsource.ParseSourceQuery(query, style)
	entries, err := projectsource.SearchSourceIndex(r.Context(), snapshot, parsed, style, rootID, position, limit+1)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	hasMore := len(entries) > limit
	entries = entries[:min(limit, len(entries))]
	matches := make([]wire.SourceSearchMatch, 0, len(entries))
	for _, entry := range entries {
		highlights := make([]wire.SourceSearchHighlight, 0, len(entry.Highlights))
		for _, span := range entry.Highlights {
			highlights = append(highlights, wire.SourceSearchHighlight{Start: span.Start, End: span.End})
		}
		matches = append(matches, wire.SourceSearchMatch{RootID: entry.RootID, Path: entry.Path, Highlights: highlights})
	}
	status := http.StatusOK
	retryAfter := 0
	warmupKey := "source-search:" + projectID
	if snapshot.Refreshing {
		if snapshot.State == projectsource.SourceIndexWarming {
			status = http.StatusAccepted
		}
		retryAfter = s.warmupPolls.warming(warmupKey)
	} else {
		s.warmupPolls.ready(warmupKey)
	}
	response := wire.SourceSearchResponse{
		State: sourceIndexWireState(snapshot.State), Revision: snapshot.Revision,
		Refreshing: snapshot.Refreshing, RetryAfterMs: retryAfter, Coverage: sourceIndexCoverage(snapshot), Matches: matches,
	}
	if parsed.Line > 0 {
		response.Location = &wire.SourceSearchLocation{Line: parsed.Line, Column: parsed.Column, EndLine: parsed.EndLine}
	}
	if outside, ok := snapshot.OutsidePath(parsed, style); ok {
		response.Outside = &wire.SourceSearchOutside{Path: outside.Path, Kind: "file"}
		if outside.Directory {
			response.Outside.Kind = "directory"
		}
	}
	if hasMore {
		nextCursor, err := encodeSourceSearchCursor(scope, snapshot.Revision, entries[len(entries)-1].SourceIndexEntry)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		response.NextCursor = nextCursor
	}
	httpio.WriteJSON(w, status, response)
}

func (s *Analysis) HandleListProjectSourceSymbols(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	pathQuery := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathQuery == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	result, err := projectsource.ListProjectSourceSymbols(r.Context(), p, projectsource.SourceSymbolsRequest{
		Path:   pathQuery,
		RootID: strings.TrimSpace(r.URL.Query().Get("root_id")),
	})
	if err != nil {
		s.writeSourceAnalysisError(w, r, err)
		return
	}
	symbols := make([]wire.SourceSymbol, 0, len(result.Symbols))
	for _, sym := range result.Symbols {
		symbols = append(symbols, wire.SourceSymbol{
			Name: sym.Name,
			Kind: wire.SourceSymbolKind(sym.Kind),
			Line: sym.Line,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceSymbolsResponse{
		SHA256:    result.SHA256,
		Symbols:   symbols,
		Truncated: result.Truncated,
	})
}

func (s *Analysis) HandleResolveProjectSourceDefinition(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	var req wire.SourceDefinitionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	pathQuery := strings.TrimSpace(req.Path)
	symbol := strings.TrimSpace(req.Symbol)
	if pathQuery == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "path is required")
		return
	}
	if symbol == "" {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "symbol is required")
		return
	}
	excludeDirs := SearchDependencyPatterns()
	if req.IncludeDependencies {
		excludeDirs = nil
	}
	result, err := projectsource.ResolveProjectSourceDefinitions(r.Context(), p, projectsource.SourceDefinitionRequest{
		RootID:      strings.TrimSpace(req.RootID),
		Path:        pathQuery,
		Symbol:      symbol,
		Line:        req.Line,
		ExcludeDirs: excludeDirs,
	}, declarationSearchIn(nil, nil, nil, req.IncludeDependencies))
	if err != nil {
		s.writeSourceAnalysisError(w, r, err)
		return
	}
	candidates := make([]wire.SourceDefinitionCandidate, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		candidates = append(candidates, wire.SourceDefinitionCandidate{
			RootID:  c.RootID,
			Path:    c.Path,
			Line:    c.Line,
			Kind:    wire.SourceSymbolKind(c.Kind),
			Snippet: c.Snippet,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceDefinitionResponse{
		Candidates: candidates,
		Truncated:  result.Truncated,
	})
}

// WriteProjectSourceError answers a failed source mutation: addressing, the
// write's own refusals, and the encoding a rewrite preserves.
