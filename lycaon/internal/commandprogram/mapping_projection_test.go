package commandprogram

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func mappingMemory(t *testing.T, state int64) *toolcontract.ProgramMemory {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1, State: state})
	testutil.FailErr(t, "program memory", err)
	return memory
}

func candidateMappingFixture() toolcontract.StreamSourceMapping {
	digest := sha256.Sum256(bytes.Repeat([]byte{'x'}, 100))
	return toolcontract.StreamSourceMapping{Source: toolcontract.SourcePin{SourceID: "fixture-source", Revision: "fixture-revision", Representation: toolcontract.LogicalUTF8LF, SHA256: hex.EncodeToString(digest[:]), StartByte: 0, EndByte: 100}, SourceStartByte: 30, SourceEndByte: 40, OutputStartByte: 10, OutputEndByte: 20}
}

func TestExactPresentationClipsCandidateRangesWithoutChangingCaptureIdentity(t *testing.T) {
	memory := mappingMemory(t, 1<<20)
	input := []toolcontract.StreamSourceMapping{candidateMappingFixture()}
	out, err := ProjectStreamMappings(t.Context(), memory, input, 12, 4, MappingExactSlice)
	testutil.FailErr(t, "project exact displayed range", err)
	defer out.Close()
	if len(out.Mappings) != 1 || memory.InUse().State == 0 {
		t.Fatal("exact projection lost mapping or accounting")
	}
	got := out.Mappings[0]
	if got.Source != input[0].Source || got.SourceStartByte != 32 || got.SourceEndByte != 36 || got.OutputStartByte != 0 || got.OutputEndByte != 4 {
		t.Fatalf("wrong exact range: %+v", got)
	}
	input[0].Source.Revision = "changed"
	if out.Mappings[0].Source.Revision != "fixture-revision" {
		t.Fatal("candidate snapshot changed with caller data")
	}
	testutil.FailErr(t, "release mapping metadata", out.Close())
	if memory.InUse().State != 0 {
		t.Fatal("mapping metadata lease leaked")
	}
}

func TestUnseenArbitraryAndSourceLikeTextDoNotCreateDeliveryMappings(t *testing.T) {
	memory := mappingMemory(t, 1<<20)
	for _, transform := range []MappingTransform{MappingUnspecified, MappingArbitrary} {
		out, err := ProjectStreamMappings(t.Context(), memory, []toolcontract.StreamSourceMapping{candidateMappingFixture()}, 0, 100, transform)
		testutil.FailErr(t, "drop arbitrary transform candidates", err)
		if len(out.Mappings) != 0 || memory.InUse().State != 0 {
			t.Fatal("arbitrary transform retained source eligibility")
		}
		testutil.FailErr(t, "close empty mapping", out.Close())
	}
	out, err := ProjectStreamMappings(t.Context(), memory, nil, 0, 100, MappingExactSlice)
	testutil.FailErr(t, "no source candidates for printed source-like bytes", err)
	if len(out.Mappings) != 0 || memory.InUse().State != 0 {
		t.Fatal("presentation invented source evidence")
	}
	testutil.FailErr(t, "release empty metadata", out.Close())
	out, err = ProjectStreamMappings(t.Context(), memory, []toolcontract.StreamSourceMapping{candidateMappingFixture()}, 0, 8, MappingExactSlice)
	testutil.FailErr(t, "preview excludes mapped unseen range", err)
	if len(out.Mappings) != 0 {
		t.Fatal("unseen retained range became displayed range")
	}
	testutil.FailErr(t, "release unmapped preview", out.Close())
	// No API in this projection records response IDs or source-owner observations.
	// Real delivered-source qualification remains at that existing owner boundary.
}

func TestMappingQuotaAndInvalidRangesReturnNoCandidateOutput(t *testing.T) {
	var refused *toolcontract.ProgramMemoryLimitError
	input := []toolcontract.StreamSourceMapping{candidateMappingFixture()}
	if out, err := ProjectStreamMappings(t.Context(), mappingMemory(t, 1), input, 10, 10, MappingExactSlice); out != nil || !errors.As(err, &refused) {
		t.Fatalf("mapping quota refusal=%v", err)
	}
	memory := mappingMemory(t, 1<<20)
	input[0].SourceEndByte = 101
	if out, err := ProjectStreamMappings(t.Context(), memory, input, 10, 10, MappingExactSlice); out != nil || err == nil {
		t.Fatal("mapping outside captured source accepted")
	}
	if memory.InUse().State != 0 {
		t.Fatal("invalid mapping retained quota")
	}
}
