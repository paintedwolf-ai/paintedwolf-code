package interp

import "fmt"

// RecordLimitError identifies a record that cannot fit the configured scanner.
type RecordLimitError struct{ Maximum, Observed int }

func (e *RecordLimitError) Error() string {
	return fmt.Sprintf("input record exceeds %d bytes", e.Maximum)
}

// retainLiveValues runs between completed records, when user calls have
// unwound. It discards dead stack references, then reconciles the same budget
// with reachable value storage. Backing strings are actual retained owners;
// shared backing may be charged more than once, never inferred from GC state.
func (p *interp) retainLiveValues() error {
	if p.budget == nil {
		return nil
	}
	for i := p.sp; i < len(p.stack); i++ {
		p.stack[i] = value{}
	}
	if len(p.arrayHighWater) < len(p.arrays) {
		if err := p.allocate(len(p.arrays) * 8); err != nil {
			return err
		}
		high := make([]int, len(p.arrays))
		copy(high, p.arrayHighWater)
		p.arrayHighWater = high
	}
	bytes := cap(p.arrays)*8 + cap(p.localArrays)*24 + cap(p.globals)*48 + cap(p.stack)*48 + cap(p.fields)*16 + cap(p.fieldsIsTrueStr) + cap(p.arrayHighWater)*8
	count := func(values []value) {
		for _, v := range values {
			bytes += len(v.s) + len(v.backing)
		}
	}
	count(p.globals)
	count(p.stack[:p.sp])
	count(p.frame)
	bytes += len(p.line) + len(p.fieldBacking)
	for _, field := range p.fields {
		bytes += len(field)
	}
	for i, array := range p.arrays {
		if len(array) > p.arrayHighWater[i] {
			p.arrayHighWater[i] = len(array)
		}
		bytes += p.arrayHighWater[i] * elementOverhead
		for key, v := range array {
			bytes += len(key) + len(v.s) + len(v.backing)
		}
	}
	// Special strings and cache keys can outlive the input record too.
	for _, text := range []string{p.filename.s, p.fieldSep, p.savedFieldSep, p.recordSep, p.recordTerminator, p.outputFieldSep, p.outputRecordSep, p.subscriptSep, p.convertFormat.format, p.outputFormat.format} {
		bytes += len(text)
	}
	for key := range p.regexCache {
		bytes += len(key)
	}
	for key := range p.formatCache {
		bytes += len(key)
	}
	return p.budget.Retain(bytes)
}
