package textutil

import (
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
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
	err = sortInto(inv, order, delimiter, nativeutility.NewOutput(inv.ctx, stage, "stream_bytes", inv.limit("stream_bytes")))
	if errors.Is(err, errMissingInput) {
		return stage.Abandon(context.WithoutCancel(inv.ctx))
	}
	return inv.settleStage(stage, err)
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
		if inv.status != 0 {
			inv.status = 2
			return errMissingInput
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
	held       map[SpillRun]spillReservation
	spilled    int64
}

type spillReservation struct {
	size    int64
	release func()
}

func (s *sorter) read(r io.Reader) error {
	records, err := s.inv.records(r, s.delimiter)
	if err != nil {
		return err
	}
	defer records.Close()
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
		return tools.CommandLimitExceeded("sort_memory_bytes", s.inv.limit("sort_memory_bytes"), s.chunkBytes)
	}
	slices.SortStableFunc(s.chunk, s.order.compare)
	var size int64
	for _, record := range s.chunk {
		size += int64(len(record)) + binary.MaxVarintLen64
	}
	run, err := s.newRun(size)
	if err != nil {
		return err
	}
	s.runs = append(s.runs, run)
	for _, record := range s.chunk {
		if err := run.WriteRecord(record); err != nil {
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

// newRun admits the complete known run before creating it. One reservation per
// run keeps admission bookkeeping independent of its number of records.
func (s *sorter) newRun(size int64) (SpillRun, error) {
	if limit := s.inv.limit("spill_bytes"); s.spilled+size > limit {
		return nil, tools.CommandLimitExceeded("spill_bytes", limit, s.spilled+size)
	}
	release, err := s.inv.ports.Admission.ReserveSpill(s.inv.ctx, size)
	if err != nil {
		return nil, err
	}
	run, err := s.inv.ports.Spills.Create(s.inv.ctx)
	if err != nil {
		release()
		return nil, err
	}
	if s.held == nil {
		s.held = map[SpillRun]spillReservation{}
	}
	s.held[run] = spillReservation{size: size, release: release}
	s.spilled += size
	return run, nil
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
			var size int64
			for _, run := range group {
				size += s.held[run].size
			}
			run, err := s.newRun(size)
			if err != nil {
				return err
			}
			err = s.mergeRuns(group, func(record []byte) error { return run.WriteRecord(record) })
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

func (s *sorter) mergeRuns(runs []SpillRun, visit func([]byte) error) (finalErr error) {
	maximum := s.inv.limit("sort_merge_fan_in")
	if int64(len(runs)) > maximum {
		return tools.CommandLimitExceeded("sort_merge_fan_in", maximum, int64(len(runs)))
	}
	release, err := s.inv.ports.Memory.Reserve(s.inv.ctx, int64(len(runs))*16)
	if err != nil {
		return err
	}
	defer release()
	readers := make([]RunReader, 0, len(runs))
	defer func() {
		for _, reader := range readers {
			if closeErr := reader.Close(); closeErr != nil {
				if finalErr == nil {
					finalErr = closeErr
				} else {
					finalErr = errors.Join(finalErr, closeErr)
				}
			}
		}
	}()
	h := &mergeHeap{order: s.order}
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
	s.held[run].release()
	delete(s.held, run)
}

func (s *sorter) cleanup() {
	for run := range s.held {
		s.remove(run)
	}
	for _, release := range s.releases {
		release()
	}
}
