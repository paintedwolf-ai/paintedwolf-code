package sourcecatalog

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Range pages are private mutable storage for one ephemeral projection. Source
// structure generations use structuralSegments instead.
const rangeSchema = `
CREATE TABLE IF NOT EXISTS range_pages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL, name TEXT NOT NULL, body BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS range_page_owner ON range_pages(scope,name,id);
CREATE TABLE IF NOT EXISTS range_roots (
 scope TEXT NOT NULL, name TEXT NOT NULL, page INTEGER NOT NULL,
 listed INTEGER NOT NULL, complete INTEGER NOT NULL, failure TEXT NOT NULL,
 PRIMARY KEY(scope,name)) WITHOUT ROWID;
`

const headGeneration = math.MaxInt64

type TreeItem struct {
	Path     string
	Symlink  bool
	Sequence int64
}

type DirectoryState struct {
	Listed   bool
	Complete bool
	Failure  string
}

func stateOf(observation DirectoryObservation) DirectoryState {
	return DirectoryState{Listed: observation.FirstListed > 0, Complete: observation.Complete, Failure: observation.Failure}
}

type rangeTx interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type RangePages struct {
	tx          rangeTx
	fresh       map[uint64]struct{}
	scope, name string
	cache       *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]
}

func newRangeReader(tx rangeTx, cache *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]) RangePages {
	return RangePages{tx: tx, cache: cache}
}

func newRangeWriter(tx rangeTx, cache *pagedview.Cache[uint64, pagedview.RangePage[TreeItem]]) RangePages {
	return RangePages{tx: tx, fresh: make(map[uint64]struct{}), cache: cache}
}

func (s RangePages) Read(ctx context.Context, id uint64) (pagedview.RangePage[TreeItem], error) {
	if s.cache != nil {
		if page, ok := s.cache.Get(id); ok {
			return page, nil
		}
	}
	var page pagedview.RangePage[TreeItem]
	var body []byte
	err := s.tx.QueryRowContext(ctx, "SELECT body FROM range_pages WHERE id=?", id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return page, pagedview.ErrMissing
	}
	if err != nil {
		return page, err
	}
	page, err = decodeRangePage(body)
	if err == nil && s.cache != nil {
		s.cache.Put(id, page, rangePageBytes(page))
	}
	return page, err
}

func (s RangePages) Write(ctx context.Context, id uint64, page pagedview.RangePage[TreeItem]) (uint64, error) {
	if s.fresh == nil {
		return 0, errors.New("range pages opened for reading cannot publish")
	}
	body, err := encodeRangePage(page)
	if err != nil {
		return 0, err
	}
	if _, ok := s.fresh[id]; ok {
		if _, err := s.tx.ExecContext(ctx, "UPDATE range_pages SET body=? WHERE id=?", body, id); err != nil {
			return 0, err
		}
		s.remember(id, page)
		return id, nil
	}
	var created uint64
	if err := s.tx.QueryRowContext(ctx, "INSERT INTO range_pages(scope,name,body) VALUES(?,?,?) RETURNING id", s.scope, s.name, body).Scan(&created); err != nil {
		return 0, err
	}
	if id != 0 {
		if _, err := s.tx.ExecContext(ctx, "DELETE FROM range_pages WHERE id=?", id); err != nil {
			return 0, err
		}
	}
	s.fresh[created] = struct{}{}
	s.remember(created, page)
	return created, nil
}

func (s RangePages) remember(id uint64, page pagedview.RangePage[TreeItem]) {
	if s.cache != nil {
		s.cache.Put(id, page, rangePageBytes(page))
	}
}

func (s RangePages) Delete(ctx context.Context, id uint64) error {
	if s.fresh == nil {
		return errors.New("range pages opened for reading cannot publish")
	}
	delete(s.fresh, id)
	if s.cache != nil {
		s.cache.Delete(id)
	}
	_, err := s.tx.ExecContext(ctx, "DELETE FROM range_pages WHERE id=?", id)
	return err
}

func (s RangePages) Open(ctx context.Context, scope, name string) (*pagedview.RangeIndex[TreeItem], DirectoryState, error) {
	s.scope, s.name = scope, name
	index := &pagedview.RangeIndex[TreeItem]{Store: s}
	var state DirectoryState
	err := s.tx.QueryRowContext(ctx, "SELECT page,listed,complete,failure FROM range_roots WHERE scope=? AND name=?", scope, name).
		Scan(&index.Root, &state.Listed, &state.Complete, &state.Failure)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return index, state, err
}

func (s RangePages) Save(ctx context.Context, scope, name string, index *pagedview.RangeIndex[TreeItem], state DirectoryState) error {
	if s.fresh == nil {
		return errors.New("range pages opened for reading cannot publish")
	}
	_, err := s.tx.ExecContext(ctx, `INSERT INTO range_roots(scope,name,page,listed,complete,failure) VALUES(?,?,?,?,?,?)
 ON CONFLICT(scope,name) DO UPDATE SET page=excluded.page,listed=excluded.listed,complete=excluded.complete,failure=excluded.failure`,
		scope, name, index.Root, state.Listed, state.Complete, state.Failure)
	return err
}

const (
	treeItemSymlink      byte = 1
	rangeBaselineMatches byte = 1
	rangePageFormat           = "observed-children-v2"
)

// The schema fingerprint identifies this ephemeral page encoding.
func encodeRangePage(page pagedview.RangePage[TreeItem]) ([]byte, error) {
	if len(page.Items) > pagedview.PageFanout || len(page.Children) > pagedview.PageFanout || len(page.Items) > 0 && len(page.Children) > 0 {
		return nil, pagedview.ErrRange
	}
	prefix := rangePathPrefix(page.Items)
	out := appendRangeString(nil, prefix)
	out = binary.AppendUvarint(out, uint64(len(page.Items)))
	for _, item := range page.Items {
		if item.Value.Sequence < 0 {
			return nil, pagedview.ErrRange
		}
		out = appendRangeString(out, item.Key)
		out = appendRangeString(out, strings.TrimPrefix(item.Value.Path, prefix))
		var flags byte
		if item.Value.Symlink {
			flags |= treeItemSymlink
		}
		out = append(out, flags)
		out = binary.AppendVarint(out, item.Value.Sequence)
		out = appendRangeMeasures(out, item.Weight, item.Unresolved, item.Fingerprint, item.BaselineFingerprint)
	}
	out = binary.AppendUvarint(out, uint64(len(page.Children)))
	for _, child := range page.Children {
		out = appendRangeString(out, child.Key)
		out = binary.AppendUvarint(out, child.Page)
		out = binary.AppendVarint(out, child.Count)
		out = appendRangeMeasures(out, child.Weight, child.Unresolved, child.Fingerprint, child.BaselineFingerprint)
	}
	return out, nil
}

func rangePathPrefix(items []pagedview.RangeItem[TreeItem]) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0].Value.Path
	for _, item := range items[1:] {
		length := 0
		for length < min(len(prefix), len(item.Value.Path)) && prefix[length] == item.Value.Path[length] {
			length++
		}
		prefix = prefix[:length]
	}
	return prefix[:strings.LastIndexByte(prefix, '/')+1]
}

func appendRangeString(out []byte, value string) []byte {
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func appendRangeMeasures(out []byte, weight, unresolved int64, fingerprint, baselineFingerprint pagedview.Fingerprint) []byte {
	out = binary.AppendVarint(out, weight)
	out = binary.AppendVarint(out, unresolved)
	out = append(out, fingerprint[:]...)
	if baselineFingerprint == fingerprint {
		return append(out, rangeBaselineMatches)
	}
	out = append(out, 0)
	return append(out, baselineFingerprint[:]...)
}

type rangeDecoder struct {
	body []byte
	err  error
}

func (d *rangeDecoder) unsigned() uint64 {
	value, n := binary.Uvarint(d.body)
	if n <= 0 {
		d.err = pagedview.ErrRange
		return 0
	}
	d.body = d.body[n:]
	return value
}

func (d *rangeDecoder) signed() int64 {
	value, n := binary.Varint(d.body)
	if n <= 0 {
		d.err = pagedview.ErrRange
		return 0
	}
	d.body = d.body[n:]
	return value
}

func (d *rangeDecoder) bytes(size uint64) []byte {
	if size > uint64(len(d.body)) {
		d.err = pagedview.ErrRange
		return nil
	}
	value := d.body[:size]
	d.body = d.body[size:]
	return value
}

func (d *rangeDecoder) text() string { return string(d.bytes(d.unsigned())) }

func (d *rangeDecoder) flags() byte {
	value := d.bytes(1)
	if len(value) != 1 {
		return 0
	}
	if value[0]&^treeItemSymlink != 0 {
		d.err = pagedview.ErrRange
	}
	return value[0]
}

func (d *rangeDecoder) measures() (weight, unresolved int64, fingerprint, baselineFingerprint pagedview.Fingerprint) {
	weight = d.signed()
	unresolved = d.signed()
	copy(fingerprint[:], d.bytes(uint64(len(fingerprint))))
	flags := d.bytes(1)
	if len(flags) == 1 {
		if flags[0]&^rangeBaselineMatches != 0 {
			d.err = pagedview.ErrRange
		} else if flags[0]&rangeBaselineMatches != 0 {
			baselineFingerprint = fingerprint
		} else {
			copy(baselineFingerprint[:], d.bytes(uint64(len(baselineFingerprint))))
		}
	}
	if weight < 0 || unresolved < 0 {
		d.err = pagedview.ErrRange
	}
	return
}

func decodeRangePage(body []byte) (pagedview.RangePage[TreeItem], error) {
	d := rangeDecoder{body: body}
	page := pagedview.RangePage[TreeItem]{}
	prefix := d.text()
	count := d.unsigned()
	if count > pagedview.PageFanout {
		return page, pagedview.ErrRange
	}
	if count > 0 {
		page.Items = make([]pagedview.RangeItem[TreeItem], 0, int(count))
	}
	for range count {
		item := pagedview.RangeItem[TreeItem]{Key: d.text()}
		item.Value.Path = prefix + d.text()
		item.Value.Symlink = d.flags()&treeItemSymlink != 0
		item.Value.Sequence = d.signed()
		if item.Value.Sequence < 0 {
			d.err = pagedview.ErrRange
		}
		item.Weight, item.Unresolved, item.Fingerprint, item.BaselineFingerprint = d.measures()
		page.Items = append(page.Items, item)
	}
	count = d.unsigned()
	if count > pagedview.PageFanout || count > 0 && len(page.Items) > 0 {
		return page, pagedview.ErrRange
	}
	if count > 0 {
		page.Children = make([]pagedview.Branch, 0, int(count))
	}
	for range count {
		child := pagedview.Branch{Key: d.text(), Page: d.unsigned(), Count: d.signed()}
		child.Weight, child.Unresolved, child.Fingerprint, child.BaselineFingerprint = d.measures()
		if child.Page == 0 || child.Count < 0 {
			d.err = pagedview.ErrRange
		}
		page.Children = append(page.Children, child)
	}
	if len(d.body) != 0 {
		d.err = pagedview.ErrRange
	}
	return page, d.err
}
