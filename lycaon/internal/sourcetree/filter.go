package sourcetree

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// Filtered is an immutable, disk-backed selection of an observed tree. It keeps
// matching visible names and their ancestors without opening additional folders.
type Filtered struct {
	refs     atomic.Int64
	rows     *sourcecatalog.ProjectionRows
	revision pagedview.Revision
	extent   pagedview.Extent
}

type filterAncestor struct {
	row       Row
	rank      int64
	emitted   bool
	ancillary *Row
}
type filterBuilder struct {
	store         *sourcecatalog.ProjectionRows
	query         string
	next          int64
	bufferedBytes int
	stack         []filterAncestor
	rows          []sourcecatalog.ProjectionRecord
	ends          map[int64]int64
}

func (v *View) Filter(ctx context.Context, query string) (*Filtered, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 4096 {
		return nil, pagedview.ErrRange
	}
	presentation, err := v.Capture(ctx)
	if err != nil {
		return nil, err
	}
	defer presentation.Close()
	if len(presentation.roots) == 0 {
		return nil, pagedview.ErrMissing
	}
	root := presentation.roots[0].root.Root
	basis, extent := presentation.Revision()
	complete := extent.Complete
	store, err := v.catalog.NewProjectionRows(ctx, v.scope.Project, root)
	if err != nil {
		return nil, err
	}
	selected := &Filtered{rows: store, revision: pagedview.Revision{Intent: basis.Intent, Projection: uuid.NewString()}, extent: pagedview.Extent{Complete: complete}}
	selected.refs.Store(1)
	builder := filterBuilder{store: store, query: query, ends: make(map[int64]int64)}
	if err := builder.build(ctx, presentation); err != nil {
		selected.Close()
		return nil, err
	}
	selected.extent.Rows = builder.next
	return selected, nil
}

func filterAddress(address Address) string { return address.Root + "\x00" + address.Path }
func (builder *filterBuilder) append(ctx context.Context, row Row) (int64, error) {
	body, err := json.Marshal(row)
	if err != nil {
		return 0, err
	}
	if len(body) > pagedview.MaxFrameBytes {
		return 0, pagedview.ErrFrameSize
	}
	if builder.bufferedBytes+len(body) > pagedview.MaxFrameBytes && len(builder.rows) > 0 {
		if err := builder.flush(ctx); err != nil {
			return 0, err
		}
	}
	builder.bufferedBytes += len(body)
	parent := ""
	if row.Address.Path != "." {
		parent = filterAddress(Address{Root: row.Address.Root, Path: path.Dir(row.Address.Path)})
	}
	auxiliary := row.Kind != "directory" && row.Kind != "file"
	if auxiliary {
		parent = filterAddress(row.Address)
	}
	rank := builder.next
	builder.rows = append(builder.rows, sourcecatalog.ProjectionRecord{Rank: rank, Address: filterAddress(row.Address), Parent: parent, Directory: row.Kind == "directory", Ancillary: auxiliary, End: rank + 1, Body: body})
	builder.next++
	if len(builder.rows) == pagedview.MaxRows {
		err = builder.flush(ctx)
	}
	return rank, err
}
func (builder *filterBuilder) flush(ctx context.Context) error {
	if len(builder.rows) == 0 && len(builder.ends) == 0 {
		return nil
	}
	if err := builder.store.Write(ctx, builder.rows, builder.ends); err != nil {
		return err
	}
	clear(builder.rows)
	builder.rows = builder.rows[:0]
	builder.bufferedBytes = 0
	clear(builder.ends)
	return nil
}
func (builder *filterBuilder) emitAncestors(ctx context.Context) error {
	for i := range builder.stack {
		ancestor := &builder.stack[i]
		if ancestor.emitted {
			continue
		}
		rank, err := builder.append(ctx, ancestor.row)
		if err != nil {
			return err
		}
		ancestor.rank, ancestor.emitted = rank, true
		if ancestor.ancillary != nil {
			if _, err := builder.append(ctx, *ancestor.ancillary); err != nil {
				return err
			}
		}
	}
	return nil
}
func (builder *filterBuilder) closeAncestors(ctx context.Context, depth int) error {
	for len(builder.stack) > depth {
		last := builder.stack[len(builder.stack)-1]
		builder.stack = builder.stack[:len(builder.stack)-1]
		if last.emitted {
			builder.ends[last.rank] = builder.next
			if len(builder.ends) == pagedview.MaxRows {
				if err := builder.flush(ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (builder *filterBuilder) consume(ctx context.Context, row Row) error {
	if err := builder.closeAncestors(ctx, row.Depth); err != nil {
		return err
	}
	if row.Kind == "directory" {
		builder.stack = append(builder.stack, filterAncestor{row: row})
		if strings.Contains(strings.ToLower(row.Name), builder.query) {
			return builder.emitAncestors(ctx)
		}
		return nil
	}
	if row.Kind == "file" {
		if !strings.Contains(strings.ToLower(row.Name), builder.query) {
			return nil
		}
		if err := builder.emitAncestors(ctx); err != nil {
			return err
		}
		_, err := builder.append(ctx, row)
		return err
	}
	if len(builder.stack) == 0 {
		return nil
	}
	parent := &builder.stack[len(builder.stack)-1]
	if parent.emitted {
		_, err := builder.append(ctx, row)
		return err
	}
	parent.ancillary = &row
	return nil
}

// Frames release snapshots before writes; a changed intent discards the materialization.
func (builder *filterBuilder) build(ctx context.Context, presentation *Presentation) error {
	for rootIndex := range presentation.roots {
		for offset := int64(0); ; {
			rows, total, err := filterSourceFrame(ctx, presentation, rootIndex, offset)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if err := builder.consume(ctx, row); err != nil {
					return err
				}
			}
			offset += int64(len(rows))
			if offset >= total {
				break
			}
			if len(rows) == 0 {
				return pagedview.ErrRange
			}
		}
	}
	if err := builder.closeAncestors(ctx, 0); err != nil {
		return err
	}
	return builder.flush(ctx)
}
func filterSourceFrame(ctx context.Context, presentation *Presentation, rootIndex int, offset int64) ([]Row, int64, error) {
	snapshot, err := presentation.snapshot(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer snapshot.Close()
	root := snapshot.roots[rootIndex]
	rows, total, err := root.projection.Frame(ctx, offset, pagedview.MaxRows)
	for i := range rows {
		if rows[i].Kind == "directory" && rows[i].Address.Path == "." {
			rows[i].Name = root.root.Label
		}
	}
	return rows, total, err
}

//nolint:contextcheck,nolintlint // Final-reference cleanup runs after filter cancellation.
func (filter *Filtered) Close() {
	if filter.refs.Add(-1) != 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = filter.rows.Release(ctx)
	filter.rows.Close()
}

// Retain duplicates an existing reference to immutable filter rows.
func (filter *Filtered) Retain() func() {
	filter.refs.Add(1)
	var once sync.Once
	return func() { once.Do(filter.Close) }
}
func (filter *Filtered) Revision() (pagedview.Revision, pagedview.Extent) {
	return filter.revision, filter.extent
}
// Locate resolves the nearest selected ancestor. An address outside the
// selection is not visible; it is not an error.
func (filter *Filtered) Locate(ctx context.Context, address Address) (Location, error) {
	if !validAddress(address) {
		return Location{}, ErrAddress
	}
	for current := address.Path; ; current = path.Dir(current) {
		row, err := filter.rows.Locate(ctx, filterAddress(Address{Root: address.Root, Path: current}))
		if err == nil {
			return Location{Address: Address{Root: address.Root, Path: current}, Index: row.Rank, Visible: current == address.Path}, nil
		}
		if !errors.Is(err, pagedview.ErrMissing) {
			return Location{}, err
		}
		if current == "." {
			return Location{Address: Address{Root: address.Root, Path: "."}}, nil
		}
	}
}
func (filter *Filtered) Frame(ctx context.Context, request FrameRequest) (Frame, error) {
	if request.Offset < 0 || request.Limit < 1 || request.Limit > pagedview.MaxRows || request.Before < 0 || request.Before >= request.Limit || request.Before > 0 && request.Anchor == nil {
		return Frame{}, pagedview.ErrRange
	}
	offset := request.Offset
	var resolved Address
	var targetIndex *int64
	if request.Anchor != nil {
		location, err := filter.Locate(ctx, *request.Anchor)
		if err != nil {
			return Frame{}, err
		}
		target, err := pagedview.AnchorOffset(location.Index, request.Offset, filter.extent.Rows, 0, true)
		if err != nil {
			return Frame{}, err
		}
		targetIndex = &target
		offset = max(0, target-int64(request.Before))
		resolved = location.Address
	}
	if offset > filter.extent.Rows {
		return Frame{}, pagedview.ErrRange
	}
	records, err := filter.rows.Frame(ctx, offset, request.Limit)
	if err != nil {
		return Frame{}, err
	}
	frame := Frame{Frame: pagedview.Frame[Row, Address]{Revision: filter.revision, Extent: filter.extent, Span: pagedview.Span{Start: offset}, Rows: []Row{}}, Ancestors: []Ancestor{}}
	frame.ResolvedAnchor, frame.Target = resolved, targetIndex
	for _, record := range records {
		var row Row
		if err := json.Unmarshal(record.Body, &row); err != nil {
			return Frame{}, err
		}
		frame.Rows = append(frame.Rows, row)
	}
	if len(records) > 0 {
		frame.Anchor = frame.Rows[0].Address
		frame.Ancestors, err = filter.ancestors(ctx, records[0])
		if err != nil {
			return Frame{}, err
		}
	}
	frame.Span.End = offset + int64(len(frame.Rows))
	return frame, nil
}
func (filter *Filtered) ancestors(ctx context.Context, record sourcecatalog.ProjectionRecord) ([]Ancestor, error) {
	var all []Ancestor
	for parent := record.Parent; parent != ""; {
		row, err := filter.rows.Locate(ctx, parent)
		if err != nil {
			return nil, err
		}
		var value Row
		if err := json.Unmarshal(row.Body, &value); err != nil {
			return nil, err
		}
		all = append(all, Ancestor{Address: value.Address, Name: value.Name, Depth: value.Depth, Index: row.Rank, End: row.End})
		parent = row.Parent
	}
	for left, right := 0, len(all)-1; left < right; left, right = left+1, right-1 {
		all[left], all[right] = all[right], all[left]
	}
	return all, nil
}

func (filter *Filtered) Find(ctx context.Context, basis, query string, offset int64, limit int, sensitive bool) (SearchPage, error) {
	if query == "" || len(query) > 4096 || offset < 0 || limit < 1 || limit > pagedview.MaxRows {
		return SearchPage{}, pagedview.ErrRange
	}
	if basis != "" && basis != filter.revision.Projection {
		return SearchPage{}, pagedview.ErrRevision
	}
	pattern := regexp.QuoteMeta(query)
	if !sensitive {
		pattern = "(?i)" + pattern
	}
	matcher := regexp.MustCompile(pattern)
	out := SearchPage{Revision: filter.revision, Matches: []SearchMatch{}, Next: offset}
	for scanned := 0; out.Next < filter.extent.Rows && scanned < 1024 && len(out.Matches) < limit; {
		records, err := filter.rows.Frame(ctx, out.Next, min(200, 1024-scanned))
		if err != nil {
			return SearchPage{}, err
		}
		if len(records) == 0 {
			return SearchPage{}, pagedview.ErrRange
		}
		for _, record := range records {
			out.Next++
			scanned++
			if record.Ancillary {
				continue
			}
			var row Row
			if err := json.Unmarshal(record.Body, &row); err != nil {
				return SearchPage{}, err
			}
			match := matcher.FindStringIndex(row.Name)
			if match != nil {
				out.Matches = append(out.Matches, SearchMatch{Address: row.Address, Index: record.Rank, From: utf16Width(row.Name[:match[0]]), To: utf16Width(row.Name[:match[1]])})
				if len(out.Matches) == limit {
					break
				}
			}
		}
	}
	out.Complete = out.Next >= filter.extent.Rows
	return out, nil
}
