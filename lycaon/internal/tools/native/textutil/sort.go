package textutil

import (
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"

	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// recordOverhead conservatively covers one retained record's slice header and
// sort bookkeeping beyond its bytes.
const recordOverhead = 48

// runSort sorts every input record. Records accumulate under
// sort_memory_bytes; a full chunk is sorted and spilled as a run, and runs
// merge with at most sort_merge_fan_in open readers per pass. Every spilled
// byte is admitted first; spills are removed whether the sort completes,
// fails or is canceled. A missing input is status 2 with no output. -c checks
// order instead; -o publishes the complete output, staged before any input is
// pinned so it may name an input.
func runSort(inv *invocation) error {
	order, err := parseSortOrder(inv)
	if err != nil {
		return err
	}
	delimiter := byte('\n')
	if inv.flag("-z") {
		delimiter = 0
	}
	if inv.flag("-c") {
		return checkSorted(inv, order, delimiter)
	}
	output, ok := inv.value("-o")
	if !ok {
		if err := sortInto(inv, order, delimiter, inv.out); !errors.Is(err, errMissingInput) {
			return err
		}
		return nil
	}
	stage, err := inv.openDestination(output)
	if err != nil {
		return err
	}
	err = sortInto(inv, order, delimiter, nativeutility.NewOutput(inv.ctx, stage, inv.limit("stream_bytes")))
	if errors.Is(err, errMissingInput) {
		return stage.Abandon(context.WithoutCancel(inv.ctx))
	}
	return settleStage(inv.ctx, stage, err)
}

func sortInto(inv *invocation, order sortOrder, delimiter byte, out io.Writer) error {
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	if inv.status != 0 {
		inv.status = 2
		return errMissingInput
	}
	s := &sorter{inv: inv, order: order, delimiter: delimiter, out: out}
	defer s.cleanup()
	for _, source := range sources {
		if err := s.read(source.reader); err != nil {
			return err
		}
	}
	if len(s.runs) == 0 {
		return s.emit(s.chunk)
	}
	if err := s.spill(); err != nil {
		return err
	}
	return s.merge()
}

// errMissingInput abandons -o output when an input could not be read; the
// status and stderr already describe the failure.
var errMissingInput = errors.New("sort input missing")

type sorter struct {
	inv        *invocation
	out        io.Writer
	order      sortOrder
	delimiter  byte
	chunk      [][]byte
	chunkBytes int64
	releases   []func()
	runs       []SpillRun
	held       map[SpillRun][]func()
	spilled    int64
}

func (s *sorter) read(r io.Reader) error {
	records, err := s.inv.records(r, s.delimiter)
	if err != nil {
		return err
	}
	budget := s.inv.limit("sort_memory_bytes")
	for {
		record, err := records.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		size := int64(len(record)) + recordOverhead
		if s.chunkBytes+size > budget {
			if err := s.spill(); err != nil {
				return err
			}
		}
		release, err := s.inv.ports.Memory.Reserve(s.inv.ctx, size)
		if err != nil {
			return err
		}
		s.releases = append(s.releases, release)
		s.chunk = append(s.chunk, slices.Clone(record))
		s.chunkBytes += size
	}
}

// emit writes records in order, dropping key duplicates under -u.
func (s *sorter) emit(records [][]byte) error {
	slices.SortStableFunc(records, s.order.compare)
	var previous []byte
	for i, record := range records {
		if s.order.unique && i > 0 && s.order.compareKeys(previous, record) == 0 {
			continue
		}
		if err := writeRecord(s.out, record, s.delimiter); err != nil {
			return err
		}
		previous = record
	}
	return nil
}

// spill sorts the current chunk into a new run and releases its memory.
func (s *sorter) spill() error {
	if len(s.chunk) == 0 {
		return nil
	}
	if s.inv.ports.Spills == nil || s.inv.ports.Admission == nil {
		return nativeutility.LimitExceeded("sort_memory_bytes", s.inv.limit("sort_memory_bytes"), s.chunkBytes)
	}
	slices.SortStableFunc(s.chunk, s.order.compare)
	run, err := s.newRun()
	if err != nil {
		return err
	}
	for _, record := range s.chunk {
		if err := s.writeRun(run, record); err != nil {
			return err
		}
	}
	if err := run.Finish(); err != nil {
		return err
	}
	for _, release := range s.releases {
		release()
	}
	s.chunk, s.chunkBytes, s.releases = nil, 0, nil
	return nil
}

func (s *sorter) newRun() (SpillRun, error) {
	run, err := s.inv.ports.Spills.Create(s.inv.ctx)
	if err != nil {
		return nil, err
	}
	s.runs = append(s.runs, run)
	return run, nil
}

// writeRun admits the framed record against spill_bytes and the program's
// spill quota before writing it.
func (s *sorter) writeRun(run SpillRun, record []byte) error {
	size := int64(len(record)) + binary.MaxVarintLen64
	if limit := s.inv.limit("spill_bytes"); s.spilled+size > limit {
		return nativeutility.LimitExceeded("spill_bytes", limit, s.spilled+size)
	}
	release, err := s.inv.ports.Admission.ReserveSpill(s.inv.ctx, size)
	if err != nil {
		return err
	}
	if s.held == nil {
		s.held = map[SpillRun][]func(){}
	}
	s.held[run] = append(s.held[run], release)
	s.spilled += size
	return run.WriteRecord(record)
}

// merge combines runs in passes of at most sort_merge_fan_in readers, then
// streams the final pass to the output.
func (s *sorter) merge() error {
	fanIn := int(s.inv.limit("sort_merge_fan_in"))
	for len(s.runs) > fanIn {
		var next []SpillRun
		for start := 0; start < len(s.runs); start += fanIn {
			group := s.runs[start:min(start+fanIn, len(s.runs))]
			if len(group) == 1 {
				next = append(next, group[0])
				continue
			}
			run, err := s.inv.ports.Spills.Create(s.inv.ctx)
			if err != nil {
				return err
			}
			err = s.mergeRuns(group, func(record []byte) error { return s.writeRun(run, record) })
			if err == nil {
				err = run.Finish()
			}
			for _, done := range group {
				s.remove(done)
			}
			next = append(next, run)
			if err != nil {
				s.runs = next
				return err
			}
		}
		s.runs = next
	}
	var previous []byte
	first := true
	return s.mergeRuns(s.runs, func(record []byte) error {
		if s.order.unique && !first && s.order.compareKeys(previous, record) == 0 {
			return nil
		}
		first = false
		previous = slices.Clone(record)
		return writeRecord(s.out, record, s.delimiter)
	})
}

func (s *sorter) mergeRuns(runs []SpillRun, visit func([]byte) error) error {
	h := &mergeHeap{order: s.order}
	readers := make([]RunReader, 0, len(runs))
	defer func() {
		for i := len(readers) - 1; i >= 0; i-- {
			_ = readers[i].Close()
		}
	}()
	for index, run := range runs {
		reader, err := run.Open(s.inv.limit("record_bytes"), s.inv.reserve)
		if err != nil {
			return err
		}
		readers = append(readers, reader)
		cursor := &mergeCursor{reader: reader, index: index}
		if ok, err := cursor.advance(); err != nil {
			return err
		} else if ok {
			heap.Push(h, cursor)
		}
	}
	for h.Len() > 0 {
		if err := s.inv.ctx.Err(); err != nil {
			return err
		}
		cursor := h.cursors[0]
		if err := visit(cursor.record); err != nil {
			return err
		}
		ok, err := cursor.advance()
		if err != nil {
			return err
		}
		if ok {
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}
	return nil
}

// remove deletes a run and returns its spill admission.
func (s *sorter) remove(run SpillRun) {
	_ = run.Remove()
	for _, release := range s.held[run] {
		release()
	}
	delete(s.held, run)
}

func (s *sorter) cleanup() {
	for _, run := range s.runs {
		s.remove(run)
	}
	for _, release := range s.releases {
		release()
	}
}
