package sourceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcetree"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sourceFrameQuery struct {
	offset int64
	limit  int
	anchor string
	before int
	retain []sourcetree.FramePrefix
}

func parseSourceFrameQuery(r *http.Request) (sourceFrameQuery, error) {
	var query sourceFrameQuery
	var err error
	query.offset, _, err = httpio.OptionalInt64Query(r, "offset", 0)
	if err != nil {
		return query, err
	}
	var present bool
	query.limit, present, err = httpio.OptionalIntQuery(r, "limit", 1, pagedview.MaxRows)
	if err != nil {
		return query, err
	}
	if !present {
		query.limit = pagedview.MaxRows
	}
	query.anchor, _, err = httpio.SingleQueryValue(r, "anchor")
	if err != nil {
		return query, err
	}
	query.before, _, err = httpio.OptionalIntQuery(r, "context_before", 0, query.limit-1)
	if err != nil {
		return query, err
	}
	if query.before > 0 && query.anchor == "" {
		return query, &httpio.QueryParameterError{Parameter: "context_before", Reason: "requires an anchor"}
	}
	query.retain, err = parseTreeRetention(r)
	return query, err
}

func parseTreeRetention(r *http.Request) ([]sourcetree.FramePrefix, error) {
	value, _, err := httpio.SingleQueryValue(r, "retain")
	if err != nil || value == "" {
		return nil, err
	}
	rejected := &httpio.QueryParameterError{Parameter: "retain", Reason: "requires at most 32 tree prefix proofs"}
	if len(value) > 8192 {
		return nil, rejected
	}
	var proofs []wire.SourceTreeFramePrefix
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proofs); err != nil || proofs == nil || len(proofs) > 32 {
		return nil, rejected
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, rejected
	}
	result := make([]sourcetree.FramePrefix, 0, len(proofs))
	for _, proof := range proofs {
		decoded, err := hex.DecodeString(proof.Fingerprint)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != proof.Fingerprint || proof.End <= 0 {
			return nil, rejected
		}
		result = append(result, sourcetree.FramePrefix{End: proof.End, Fingerprint: proof.Fingerprint})
	}
	return result, nil
}

func decodeSourceAnchor[T any](encoded string, required ...string) (*T, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 131072 {
		return nil, pagedview.ErrRange
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, pagedview.ErrRange
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, pagedview.ErrRange
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, pagedview.ErrRange
	}
	for _, name := range required {
		member, exists := members[name]
		if !exists || bytes.Equal(bytes.TrimSpace(member), []byte("null")) {
			return nil, pagedview.ErrRange
		}
	}
	var out T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return nil, pagedview.ErrRange
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, pagedview.ErrRange
	}
	return &out, nil
}

func (s *Handler) HandleGetSourceViewRows(w http.ResponseWriter, r *http.Request) {
	view, r, release, ok := s.requestedSourcePresentation(w, r)
	if !ok {
		return
	}
	defer release()
	query, err := parseSourceFrameQuery(r)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	encoded, err := s.sourceFrame(r, chi.URLParam(r, "presentation_id"), view, query)

	if err != nil {
		s.writeSourceViewError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (view *sourceViewRead) treeFrame(r *http.Request, query sourceFrameQuery) (*wire.SourceTreeFrame, error) {
	if view.reviewPreparing {
		return nil, treeReviewPreparing()
	}
	anchor, err := decodeSourceAnchor[wire.SourceTreeAddress](query.anchor, "root_id", "path")
	if err != nil {
		return nil, err
	}
	request := sourcetree.FrameRequest{Offset: query.offset, Limit: query.limit, Before: query.before, Retain: query.retain}
	if anchor != nil {
		address := treeAddress(*anchor)
		request.Anchor = &address
	}
	var frame sourcetree.Frame
	if view.treeIntent.Filter != "" {
		if view.filtered == nil {
			return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source filter is being prepared."}
		}
		frame, err = view.filtered.Frame(r.Context(), request)
	} else {
		frame, err = view.presentation.Frame(r.Context(), request)
	}
	if err != nil {
		return nil, err
	}
	out := &wire.SourceTreeFrame{Kind: "tree", ViewID: view.id, IntentRevision: view.intentRevision, ProjectionRevision: frame.Revision.Projection,
		Target: frame.Target, Extent: wireViewExtent(frame.Extent), Span: wire.SourceViewSpan{Start: frame.Span.Start, End: frame.Span.End}, Anchor: wireTreeAddress(frame.Anchor),
		Rows: make([]wire.SourceTreeRow, 0, len(frame.Rows)), Ancestors: make([]wire.SourceTreeAncestor, 0, len(frame.Ancestors))}
	if frame.Prefix != nil {
		out.Prefix = &wire.SourceTreeFramePrefix{End: frame.Prefix.End, Fingerprint: frame.Prefix.Fingerprint}
	}
	if frame.RetainedPrefix != nil {
		out.RetainedPrefix = &wire.SourceTreeFramePrefix{End: frame.RetainedPrefix.End, Fingerprint: frame.RetainedPrefix.Fingerprint}
	}
	for _, row := range frame.Rows {
		out.Rows = append(out.Rows, wireTreeRow(row))
	}
	for _, ancestor := range frame.Ancestors {
		out.Ancestors = append(out.Ancestors, wire.SourceTreeAncestor{Index: ancestor.Index, End: ancestor.End,
			Row: wire.SourceTreeRow{Address: wireTreeAddress(ancestor.Address), Name: ancestor.Name, Kind: "directory", Depth: ancestor.Depth, Expanded: true, Deleted: ancestor.Deleted}})
	}
	if out.Anchor.RootID == "" && len(view.roots) > 0 {
		out.Anchor = wire.SourceTreeAddress{RootID: view.roots[0].ID, Path: "."}
	}
	rows := out.Rows
	out.Rows = nil
	envelope, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	out.Rows, err = pagedview.Pack(rows, len(envelope)+128)
	out.Span.End = out.Span.Start + int64(len(out.Rows))
	return out, err
}

func wireTreeRow(row sourcetree.Row) wire.SourceTreeRow {
	return wire.SourceTreeRow{Address: wireTreeAddress(row.Address), Name: row.Name, Kind: row.Kind, Depth: row.Depth,
		Expanded: row.Expanded, Symlink: row.Symlink, Deleted: row.Deleted, Error: row.Error}
}

func (view *sourceViewRead) comparisonFrame(r *http.Request, query sourceFrameQuery) (*wire.SourceComparisonFrame, error) {
	if len(query.retain) > 0 {
		return nil, pagedview.ErrRange
	}
	if view.state != "ready" {
		return nil, &comparisonFailure{wire.ApiErrorCodeSourceViewPreparing, "The source comparison is not ready."}
	}
	anchor, err := decodeSourceAnchor[wire.SourceComparisonAnchor](query.anchor, "row")
	if err != nil {
		return nil, err
	}
	revision := viewProjectionRevision(view.intentRevision, view.projectionRevision)
	out := &wire.SourceComparisonFrame{Kind: "comparison", ViewID: view.id, IntentRevision: view.intentRevision, ProjectionRevision: revision,
		Extent: wire.SourceViewExtent{Complete: true}, Rows: []wire.SourceReaderRow{}, Span: wire.SourceViewSpan{Start: query.offset, End: query.offset}}
	if view.projection == nil {
		return out, nil
	}
	offset := query.offset
	if anchor != nil {
		rank, _, err := view.projection.Locate(r.Context(), anchor.Row)
		if err != nil {
			return nil, err
		}
		total, err := view.projection.Extent(r.Context())
		if err != nil {
			return nil, err
		}
		target, err := pagedview.AnchorOffset(rank, query.offset, total, 0, true)
		if err != nil {
			return nil, err
		}
		out.Target = &target
		offset = max(0, target-int64(query.before))
	}
	rows, total, err := view.projection.Frame(r.Context(), offset, query.limit)
	if err != nil {
		return nil, err
	}
	out.Extent.Rows, out.Span.Start = total, offset
	for _, row := range rows {
		out.Rows = append(out.Rows, row.Source)
	}
	if len(out.Rows) > 0 {
		out.Anchor.Row = out.Rows[0].Index
	}
	packed := out.Rows
	out.Rows = nil
	envelope, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode comparison frame: %w", err)
	}
	out.Rows, err = pagedview.Pack(packed, len(envelope)+128)
	out.Span.End = offset + int64(len(out.Rows))
	return out, err
}
