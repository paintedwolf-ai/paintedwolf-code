package search

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

func TestCommandsLearnContentWithoutCatalogPreparation(t *testing.T) {
	for _, argv := range [][]string{
		{"grep", "-ril", "needletoken", "src"},
		{"grep", "-riE", "needle(Token|Other)", "src"},
		{"rg", "-i", "needletoken", "src"},
		{"grep", "-riF", "NeedleToken", "src"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			h := newHarness(t, indexCorpus())
			h.owners.Catalog = sourcecatalog.New()
			cold := h.exec("", argv...)
			warm := h.exec("", argv...)
			if cold.err != nil || warm.err != nil || cold.stdout != warm.stdout || cold.stderr != warm.stderr || cold.result != warm.result {
				t.Fatalf("cold=%+v warm=%+v", cold, warm)
			}
			if coldN, warmN := len(h.pins.paths(cold.execution)), len(h.pins.paths(warm.execution)); warmN >= coldN || warmN > 3 {
				t.Fatalf("captures cold=%d warm=%d", coldN, warmN)
			}
		})
	}
}

func TestIndexedByteSearchPreservesEncodedAndBinaryResults(t *testing.T) {
	files := map[string]string{"src/utf8.txt": "\ufeffNeedleToken\n", "src/utf16.txt": "\xff\xfeN\x00e\x00e\x00d\x00l\x00e\x00\n\x00", "src/invalid.txt": "\xffNeedleToken\n", "src/binary.dat": "\x00NeedleToken\n", "src/unicode.txt": "Kelvin ſource Σigma\n", "src/noise.txt": "different content\n"}
	for _, argv := range [][]string{
		{"grep", "-raF", "\ufeffNeedleToken", "src"},
		{"grep", "-raF", "N\x00e\x00e", "src"},
		{"grep", "-raF", "\xffNeedleToken", "src"},
		{"grep", "-raiE", "kelvin|source|σigma|needletoken", "src"},
		{"rg", "-ai", "needletoken", "src"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			h := newHarness(t, files)
			live := h.exec("", argv...)
			h.owners.Catalog = sourcecatalog.New()
			_ = h.exec("", "grep", "-ra", "not-present", "src")
			warm := h.exec("", argv...)
			if live.err != nil || warm.err != nil || live.stdout != warm.stdout || live.stderr != warm.stderr || live.result != warm.result {
				t.Fatalf("live=%+v warm=%+v", live, warm)
			}
		})
	}
}
