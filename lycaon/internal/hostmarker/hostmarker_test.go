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
		"EVIDENCE_COVERAGE_PATTERN":    EvidenceCoveragePattern,
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

// AC-MID-13: markers go and sentences stay, including sentences that read like
// the framing the host once wrote.
func TestStripParrotedTransportMarkers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"no marker unchanged", "  Plain\ntext\r\n", "  Plain\ntext\r\n"},
		{"model header", "⟦D:model:internal_step_note⟧\n⟦D⟧Checking the parser.", "Checking the parser."},
		{"host header", "⟦D:host:internal_step_note⟧\nChecking.", "Checking."},
		{"model typo close", "⟦D:model:internal_step_note⟦\nChecking.", "Checking."},
		{"host typo close", "⟦D:host:internal_step_note⟦\nChecking.", "Checking."},
		{"marked header", "⟦D⟧⟦D:model:internal_step_note⟧\nChecking.", "Checking."},
		{"header alone is empty", "⟦D:model:internal_step_note⟧\n⟦D⟧\n", ""},
		{"line prefixes without a header", "⟦D⟧one\n⟦D⟧⟦D⟧two", "one\ntwo"},
		{"header mid text", "First.\n⟦D:model:internal_step_note⟧\nSecond.", "First.\nSecond."},
		{"sentence without markers stays", "Internal working note, not shown to the user.", "Internal working note, not shown to the user."},
		{"sentence after a header stays", "⟦D:model:internal_step_note⟧\nInternal working note, not shown to the user.", "Internal working note, not shown to the user."},
		{"other labels are not parroted framing", "⟦D:tool:read⟧ is the old form.", "⟦D:tool:read⟧ is the old form."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := StripParrotedTransportMarkers(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvidenceCoverageTagsSplitLikePlainTags(t *testing.T) {
	for _, tc := range []struct {
		name, content, tag, handle, body string
	}{
		{"plain", "[read#7]\n{}", "[read#7]\n", "read#7", "{}"},
		{"range", "[read#7 a/b.go:10-20]\n{}", "[read#7 a/b.go:10-20]\n", "read#7", "{}"},
		{"one line", "[read#7 a.go:14]\nrest", "[read#7 a.go:14]\n", "read#7", "rest"},
		{"quoted path", "[read#7 \"a b.go\":1-2]\n{}", "[read#7 \"a b.go\":1-2]\n", "read#7", "{}"},
		{"leg handle", "[leg-a:read#7 a.go:1-2]\n{}", "[leg-a:read#7 a.go:1-2]\n", "leg-a:read#7", "{}"},
		{"prose after a handle is no tag", "[read#7 see below]\n{}", "", "", "[read#7 see below]\n{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag, body := SplitEvidenceHandleTag(tc.content)
			if tag != tc.tag || body != tc.body || EvidenceTagHandle(tag) != tc.handle {
				t.Fatalf("split = %q %q handle %q", tag, body, EvidenceTagHandle(tag))
			}
		})
	}
}

func TestFormatEvidenceCoverage(t *testing.T) {
	for _, tc := range []struct {
		path       string
		start, end int
		want       string
	}{
		{"a.go", 10, 20, "a.go:10-20"},
		{"a.go", 14, 14, "a.go:14"},
		{"a b.go", 1, 2, `"a b.go":1-2`},
		{"a]b.go", 1, 2, ""},
		{"a.go", 0, 2, ""},
	} {
		if got := FormatEvidenceCoverage(tc.path, tc.start, tc.end); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.path, got, tc.want)
		}
	}
}
