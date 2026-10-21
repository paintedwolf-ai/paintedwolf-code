package commandprogram

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// MappingTransform must be selected by the implementing operation, never by
// inspecting output text or paths. Unspecified and arbitrary transforms drop
// candidate mappings; only an exact byte slice can use the projection below.
type MappingTransform uint8

const (
	MappingUnspecified MappingTransform = iota
	MappingExactSlice
	MappingArbitrary
)

// MappingLease retains candidate metadata until its last consumer is done.
// Closing this lease is not a model-delivery event or source-owner settlement.
type MappingLease struct {
	Mappings []toolcontract.StreamSourceMapping
	release  func()
	once     sync.Once
}

func (m *MappingLease) Close() error {
	m.once.Do(func() {
		m.Mappings = nil
		if m.release != nil {
			m.release()
		}
	})
	return nil
}

// ProjectStreamMappings clips only exact source bytes into an explicitly exact
// presentation slice. Retaining full output, printing paths or inspecting a
// preview creates no mapping and no delivery evidence. Callers keep input
// immutable until this projection returns.
func ProjectStreamMappings(ctx context.Context, admission *toolcontract.ProgramMemory, input []toolcontract.StreamSourceMapping, start, length int64, transform MappingTransform) (*MappingLease, error) {
	if ctx == nil || admission == nil {
		return nil, fmt.Errorf("source mapping projection requires context and state admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transform != MappingExactSlice {
		return &MappingLease{}, nil
	}
	if start < 0 || length < 0 || start > math.MaxInt64-length {
		return nil, fmt.Errorf("invalid exact presentation range")
	}
	end := start + length
	count := 0
	charge := int64(256)
	for _, m := range input {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validStreamMapping(m); err != nil {
			return nil, err
		}
		if max(start, m.OutputStartByte) >= min(end, m.OutputEndByte) {
			continue
		}
		extra := int64(256) + int64(len(m.Source.SourceID)) + int64(len(m.Source.Revision)) + int64(len(m.Source.Representation)) + int64(len(m.Source.SHA256))
		if extra > math.MaxInt64-charge {
			return nil, fmt.Errorf("source mapping state size overflow")
		}
		charge += extra
		count++
	}
	if count == 0 {
		return &MappingLease{}, nil
	}
	release, err := admission.ReserveProgramState(ctx, charge)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	out := &MappingLease{Mappings: make([]toolcontract.StreamSourceMapping, 0, count), release: release}
	for _, m := range input {
		if err := ctx.Err(); err != nil {
			_ = out.Close()
			return nil, err
		}
		left, right := max(start, m.OutputStartByte), min(end, m.OutputEndByte)
		if left >= right {
			continue
		}
		m.SourceStartByte += left - m.OutputStartByte
		m.SourceEndByte = m.SourceStartByte + (right - left)
		m.OutputStartByte = left - start
		m.OutputEndByte = right - start
		m.Source.SourceID = strings.Clone(m.Source.SourceID)
		m.Source.Revision = strings.Clone(m.Source.Revision)
		m.Source.Representation = toolcontract.SourceRepresentation(strings.Clone(string(m.Source.Representation)))
		m.Source.SHA256 = strings.Clone(m.Source.SHA256)
		out.Mappings = append(out.Mappings, m)
	}
	return out, nil
}

func validStreamMapping(m toolcontract.StreamSourceMapping) error {
	s := m.Source
	if s.SourceID == "" || s.Revision == "" || len(s.SHA256) != 64 || s.StartByte < 0 || s.EndByte < s.StartByte || m.OutputStartByte < 0 || m.OutputEndByte < m.OutputStartByte || m.SourceStartByte < s.StartByte || m.SourceEndByte > s.EndByte || m.SourceEndByte < m.SourceStartByte || m.SourceEndByte-m.SourceStartByte != m.OutputEndByte-m.OutputStartByte {
		return fmt.Errorf("invalid exact source mapping")
	}
	var digest [32]byte
	if _, err := hex.Decode(digest[:], []byte(s.SHA256)); err != nil {
		return fmt.Errorf("invalid source mapping digest")
	}
	switch s.Representation {
	case toolcontract.UTF8SourceBytes, toolcontract.LogicalUTF8LF, toolcontract.OriginalEncodedBytes:
	default:
		return fmt.Errorf("invalid source mapping representation")
	}
	return nil
}
