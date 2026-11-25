package commandprogram

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type settledInput struct {
	io.Reader
	mappings []toolcontract.StreamSourceMapping
	settled  int
}

func (s *settledInput) SettledSourceMappings(context.Context) ([]toolcontract.StreamSourceMapping, error) {
	s.settled++
	return s.mappings, nil
}

type recordedMappings []toolcontract.StreamSourceMapping

func (r *recordedMappings) RecordSourceMapping(m toolcontract.StreamSourceMapping) error {
	*r = append(*r, m)
	return nil
}

func TestInputProjectionRebasesCopiedSpansOntoProducerMappings(t *testing.T) {
	memory := mappingMemory(t, 1<<20)
	upstream := candidateMappingFixture()
	input := &settledInput{Reader: strings.NewReader(""), mappings: []toolcontract.StreamSourceMapping{upstream}}
	projection := NewInputProjection(t.Context(), input, memory)
	// Input bytes [12,14) and [14,16) are adjacent in both streams, so they coalesce.
	projection.Copied(12, 0, 2)
	projection.Copied(14, 2, 2)
	projection.Copied(18, 7, 4)
	var got recordedMappings
	testutil.FailErr(t, "record projection", projection.Record(&got))
	if input.settled != 1 || len(got) != 2 {
		t.Fatalf("settled=%d mappings=%+v", input.settled, got)
	}
	if got[0].SourceStartByte != 32 || got[0].SourceEndByte != 36 || got[0].OutputStartByte != 0 || got[0].OutputEndByte != 4 {
		t.Fatalf("first span = %+v", got[0])
	}
	if got[1].SourceStartByte != 38 || got[1].SourceEndByte != 40 || got[1].OutputStartByte != 7 || got[1].OutputEndByte != 9 {
		t.Fatalf("clipped span = %+v", got[1])
	}
	if memory.InUse().State != 0 {
		t.Fatal("projection spans leaked memory")
	}
}

func TestInputProjectionNeedsMappedInputAndCopies(t *testing.T) {
	memory := mappingMemory(t, 1<<20)
	if NewInputProjection(t.Context(), strings.NewReader("x"), memory) != nil {
		t.Fatal("unmapped input produced a projection")
	}
	var none *InputProjection
	none.Copied(0, 0, 1)
	testutil.FailErr(t, "nil projection", none.Record(&recordedMappings{}))
	input := &settledInput{Reader: strings.NewReader("")}
	projection := NewInputProjection(t.Context(), input, memory)
	testutil.FailErr(t, "empty projection", projection.Record(&recordedMappings{}))
	if input.settled != 0 {
		t.Fatal("a stage that copied nothing waited for its producer")
	}
}

func TestInputProjectionDropsSpansPastItsMemory(t *testing.T) {
	memory := mappingMemory(t, 1)
	input := &settledInput{Reader: strings.NewReader(""), mappings: []toolcontract.StreamSourceMapping{candidateMappingFixture()}}
	projection := NewInputProjection(t.Context(), input, memory)
	projection.Copied(12, 0, 2)
	var got recordedMappings
	testutil.FailErr(t, "refused projection", projection.Record(&got))
	if len(got) != 0 || input.settled != 0 {
		t.Fatalf("refused projection recorded %+v", got)
	}
}
