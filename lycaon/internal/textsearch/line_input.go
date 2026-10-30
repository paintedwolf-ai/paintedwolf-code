package textsearch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

const scanBufferBytes = 64 << 10

type scanMemory struct {
	ctx     context.Context
	reserve func(context.Context, int64) (func(), error)
}

func (m scanMemory) acquire(n int64) (func(), error) {
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	if m.reserve == nil || n == 0 {
		return func() {}, nil
	}
	release, err := m.reserve(m.ctx, n)
	if err == nil && release == nil {
		err = fmt.Errorf("line search admission returned no lease")
	}
	return release, err
}

type scanRecordReader struct {
	reader        *bufio.Reader
	memory        scanMemory
	inputLimit    int64
	trimCR        bool
	raw           []byte
	readerRelease func()
	rawRelease    func()
}

func newScanRecordReader(ctx context.Context, input io.Reader, sel Selector, opts ScanOptions) (*scanRecordReader, error) {
	memory := scanMemory{ctx: ctx}
	var maximum int64
	if matcher, ok := sel.(*Matcher); ok {
		memory.reserve, maximum = matcher.limits.Reserve, matcher.limits.InputBytes
	}
	release, err := memory.acquire(scanBufferBytes)
	if err != nil {
		return nil, err
	}
	reader := scanReaderPool.Get().(*bufio.Reader)
	reader.Reset(input)
	return &scanRecordReader{reader: reader, memory: memory, inputLimit: maximum, trimCR: opts.TrimCR && !opts.NullData, readerRelease: release}, nil
}

func (r *scanRecordReader) close() {
	r.reader.Reset(nil)
	scanReaderPool.Put(r.reader)
	r.readerRelease()
	if r.rawRelease != nil {
		r.raw = nil
		r.rawRelease()
	}
}

func (r *scanRecordReader) check(size, start int64, maximum int64, onExceed func(int64, int64) error) error {
	if maximum > 0 && size > maximum-start {
		if onExceed != nil {
			return onExceed(maximum, start+size)
		}
		return &BudgetError{Resource: "stream_throughput_bytes", Maximum: maximum, Observed: start + size}
	}
	allowance := int64(1)
	if r.trimCR {
		allowance++
	}
	if r.inputLimit > 0 && size > r.inputLimit+allowance {
		return &BudgetError{Resource: "regex_input_bytes", Maximum: r.inputLimit, Observed: size - allowance}
	}
	return nil
}

func (r *scanRecordReader) grow(needed int) error {
	if needed <= cap(r.raw) {
		return nil
	}
	capacity := max(needed, max(scanBufferBytes, 2*cap(r.raw)))
	if r.inputLimit > 0 {
		capacity = min(capacity, int(r.inputLimit)+2)
	}
	release, err := r.memory.acquire(int64(capacity))
	if err != nil {
		return err
	}
	next := make([]byte, len(r.raw), capacity)
	copy(next, r.raw)
	oldRelease := r.rawRelease
	r.raw, r.rawRelease = next, release
	if oldRelease != nil {
		oldRelease()
	}
	return nil
}

func (r *scanRecordReader) read(separator byte, start int64, maximum int64, onExceed func(int64, int64) error) ([]byte, error) {
	if err := r.memory.ctx.Err(); err != nil {
		return nil, err
	}
	chunk, err := r.reader.ReadSlice(separator)
	if checkErr := r.check(int64(len(chunk)), start, maximum, onExceed); checkErr != nil {
		return nil, checkErr
	}
	if !errors.Is(err, bufio.ErrBufferFull) {
		return chunk, err
	}
	r.raw = r.raw[:0]
	for {
		if growErr := r.grow(len(r.raw) + len(chunk)); growErr != nil {
			return nil, growErr
		}
		r.raw = append(r.raw, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return r.raw, err
		}
		if cancelErr := r.memory.ctx.Err(); cancelErr != nil {
			return nil, cancelErr
		}
		chunk, err = r.reader.ReadSlice(separator)
		if checkErr := r.check(int64(len(r.raw)+len(chunk)), start, maximum, onExceed); checkErr != nil {
			return nil, checkErr
		}
	}
}
