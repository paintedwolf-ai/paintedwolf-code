package hostmarker

import (
	"strconv"
	"strings"
	"testing"
)

func TestCompactionBannerWrapsItsBody(t *testing.T) {
	t.Parallel()
	got := CompactionBanner("read — original ~4000 tokens")
	want := "[compacted read — original ~4000 tokens]"
	if got != want {
		t.Fatalf("CompactionBanner = %q want %q", got, want)
	}
}

func TestBannerBodyCannotCloseTheBannerEarly(t *testing.T) {
	t.Parallel()
	// A close marker inside the prefix would end the banner early.
	if strings.Contains(CompactionBannerOpen, CompactionBannerClose) {
		t.Fatal("the open marker must not contain the close marker")
	}
}

func TestAllCoversEveryMarkerConstant(t *testing.T) {
	t.Parallel()
	declared := map[string]string{}
	for _, marker := range All() {
		if marker.Name == "" || marker.Value == "" {
			t.Fatalf("marker %+v has an empty name or value", marker)
		}
		if prior, dup := declared[marker.Name]; dup {
			t.Fatalf("marker name %q declared twice (%q and %q)", marker.Name, prior, marker.Value)
		}
		declared[marker.Name] = marker.Value
	}

	for name, want := range map[string]string{
		"EVIDENCE_HANDLE_PATTERN":      EvidenceHandlePattern,
		"OVERLAY_PROMOTE_EVENT_PREFIX": OverlayPromoteEventPrefix,
		"OVERLAY_REJECT_EVENT_PREFIX":  OverlayRejectEventPrefix,
		"COMPACTION_BANNER_OPEN":       CompactionBannerOpen,
		"COMPACTION_BANNER_CLOSE":      CompactionBannerClose,
		"VERBATIM_HEAD_TAIL":           VerbatimHeadTail,
		"GUIDANCE_BLOCK_OPEN":          GuidanceBlockOpen,
		"REJECTED":                     Rejected,
		"CODE_LINE":                    CodeLine,
	} {
		got, listed := declared[name]
		if !listed {
			t.Errorf("%s is a marker constant but is absent from All() — Den never receives it", name)
			continue
		}
		if got != want {
			t.Errorf("All()[%s] = %s, want %s", name, strconv.Quote(got), strconv.Quote(want))
		}
	}
	if len(declared) != 9 {
		t.Errorf("All() holds %d markers; update this test when the set changes", len(declared))
	}
}
