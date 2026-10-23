package command

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// childOutputView keeps candidate source mappings on the child's actual output
// endpoint while separately retaining its preview. Pipeline endpoints never gain
// model-observation authority.
type childOutputView struct {
	io.Writer
	target io.Writer
	base   int64
}

func (w childOutputView) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	recorder, ok := w.target.(*boundedBuffer)
	if !ok {
		return nil
	}

	m.OutputStartByte += w.base
	m.OutputEndByte += w.base
	return recorder.RecordSourceMapping(m)
}

func (b *boundedBuffer) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Mapping metadata is bounded by the inline window. Anything outside it is
	// omitted from this delivery and grants no read.
	if m.OutputStartByte >= int64(b.limit) || len(b.mappings) >= b.limit {
		return nil
	}
	if b.overlapping {
		return nil
	}
	if b.memory != nil {
		charge := int64(256 + len(m.Source.SourceID) + len(m.Source.Revision) + len(m.Source.Representation) + len(m.Source.SHA256))
		release, err := b.memory.ReserveProgramState(b.mappingContext, charge)
		if err != nil {
			return err
		}
		b.mappingReleases = append(b.mappingReleases, release)
	}
	m.Source.SourceID = strings.Clone(m.Source.SourceID)
	m.Source.Revision = strings.Clone(m.Source.Revision)
	m.Source.SHA256 = strings.Clone(m.Source.SHA256)
	b.mappings = append(b.mappings, m)
	return nil
}

func (b *boundedBuffer) beginWriter() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.activeWriters++
	if b.activeWriters > 1 {
		b.overlapping = true
	}
	return int64(len(b.data))
}

func (b *boundedBuffer) delivered(ctx context.Context, tc tools.ToolContext, screened string) {
	b.mu.Lock()
	if b.overlapping {
		b.mu.Unlock()
		return
	}
	if b.memory != nil {
		release, err := b.memory.ReserveProgramState(ctx, int64(len(b.data)+len(b.mappings)*256))
		if err != nil {
			b.mu.Unlock()
			return
		}
		defer release()
	}
	data := append([]byte(nil), b.data...)
	mappings := append([]toolcontract.StreamSourceMapping(nil), b.mappings...)
	b.mu.Unlock()
	// Screening may replace bytes or alter positions; such output grants no read.
	if string(data) != screened {
		return
	}
	for _, m := range mappings {
		left, right := max(int64(0), m.OutputStartByte), min(int64(len(data)), m.OutputEndByte)
		if left >= right {
			continue
		}
		m.SourceStartByte += left - m.OutputStartByte
		m.SourceEndByte = m.SourceStartByte + right - left
		m.OutputStartByte, m.OutputEndByte = left, right
		tools.RememberCommandSourceDelivery(ctx, tc, b.memory, m, data[left:right])
	}
	return
}

func (d *programDocuments) appendNotes(stderr string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(d.notes))
	length := len(stderr)
	for key, note := range d.notes {
		if !note.delivered {
			keys = append(keys, key)
			length += len(note.text) + 1
		}
	}
	if len(keys) == 0 {
		return stderr
	}
	sort.Strings(keys)
	// The initial window and each notice's projection envelope remain held.
	var out strings.Builder
	out.Grow(length + 1)
	out.WriteString(stderr)
	if stderr != "" && stderr[len(stderr)-1] != '\n' {
		out.WriteByte('\n')
	}
	for _, key := range keys {
		note := d.notes[key]
		note.delivered = true
		out.WriteString(note.text)
		out.WriteByte('\n')
	}
	return out.String()
}

func (b *boundedBuffer) endWriter() { b.mu.Lock(); defer b.mu.Unlock(); b.activeWriters-- }
func (b *boundedBuffer) closeMappings() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, release := range b.mappingReleases {
		release()
	}
	b.mappingReleases = nil
	b.mappings = nil
}

// takePaths retains the existing notice admission through completion consumption,
// even after the background job releases its last program hold.
func (d *programDocuments) takePaths() ([]string, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, note := range d.notes {
		if !note.delivered {
			count++
		}
	}
	if count == 0 {
		return nil, nil
	}
	paths := make([]string, 0, count)
	for _, note := range d.notes {
		if !note.delivered {
			paths = append(paths, note.path)
			note.delivered = true
		}
	}
	sort.Strings(paths)
	d.transfers++
	return paths, sync.OnceFunc(d.releaseTransfer)
}
