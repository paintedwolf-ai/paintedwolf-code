package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
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
			info, err := os.Stat(filepath.Join(h.root, "src", "match.go"))
			testutil.FailErr(t, "native observation capability", err)
			if coldN, warmN := len(h.pins.paths(cold.execution)), len(h.pins.paths(warm.execution)); fspath.ObserveFile(info).Known && (warmN >= coldN || warmN > 3) {
				t.Fatalf("captures cold=%d warm=%d", coldN, warmN)
			}
		})
	}
}

func TestIndexedByteSearchPreservesEncodedAndBinaryResults(t *testing.T) {
	files := map[string]string{"src/utf8.txt": "\ufeffNeedleToken\n", "src/utf16.txt": "\xff\xfeN\x00e\x00e\x00d\x00l\x00e\x00\n\x00", "src/invalid.txt": "\xffNeedleToken\n", "src/binary.dat": "\x00NeedleToken\n", "src/unicode.txt": "Kelvin ſource Σigma\n", "src/cross.txt": "ébc\n", "src/noise.txt": "different content\n"}
	for _, argv := range [][]string{
		{"grep", "-raF", "\ufeffNeedleToken", "src"},
		{"grep", "-raF", "N\x00e\x00e", "src"},
		{"grep", "-raF", "\xffNeedleToken", "src"},
		{"grep", "-raF", "\xa9bc", "src"},
		{"grep", "-raiE", "kelvin|source|σigma|needletoken", "src"},
		{"rg", "-uuu", "-i", "needletoken", "src"},
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

func TestContentProofsTrackLiveVersionsAcrossAliases(t *testing.T) {
	for _, kind := range []string{"directory-link", "hard-link", "transient-outside-link"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, map[string]string{"real/file.txt": "ordinary text\n"})
			target := filepath.Join(h.root, "real", "file.txt")
			info, err := os.Stat(target)
			testutil.FailErr(t, "original file metadata", err)
			aliases := []string{"./real/file.txt"}
			writePath := target
			switch kind {
			case "directory-link":
				testutil.FailErr(t, "directory alias", os.Symlink("real", filepath.Join(h.root, "alias")))
				aliases = append(aliases, "./alias/file.txt")
			case "hard-link":
				writePath = filepath.Join(h.root, "alias.txt")
				testutil.FailErr(t, "hard-link alias", os.Link(target, writePath))
				aliases = append(aliases, "./alias.txt")
			}
			h.owners.Catalog = sourcecatalog.New()
			cold := h.exec("", "grep", "-Rl", "NeedleToken", ".")
			warm := h.exec("", "grep", "-Rl", "NeedleToken", ".")
			cold.expect(t, "cold absence", "", "", 1)
			warm.expect(t, "warm absence", "", "", 1)
			current, err := os.Stat(target)
			testutil.FailErr(t, "linked metadata", err)
			if version := fspath.ObserveFile(current); version.Known && len(h.pins.paths(warm.execution)) >= len(h.pins.paths(cold.execution)) {
				t.Fatal("current alias facts were not reused")
			}
			if kind == "transient-outside-link" {
				writePath = filepath.Join(t.TempDir(), "outside")
				testutil.FailErr(t, "outside hard link", os.Link(target, writePath))
			}
			testutil.FailErr(t, "write through alias", os.WriteFile(writePath, []byte("NeedleToken!!\n"), 0o644))
			testutil.FailErr(t, "restore modification time", os.Chtimes(target, info.ModTime(), info.ModTime()))
			if kind == "transient-outside-link" {
				testutil.FailErr(t, "retire outside alias", os.Remove(writePath))
			}
			changed := h.exec("", "grep", "-Rl", "NeedleToken", ".")
			if changed.err != nil || changed.result.Status != 0 {
				t.Fatalf("changed search=%+v", changed)
			}
			for _, alias := range aliases {
				if !strings.Contains(changed.stdout, alias+"\n") {
					t.Fatalf("alias %s was excluded: %q", alias, changed.stdout)
				}
			}
		})
	}
}

func TestCommandZeroSizeInputsStayLive(t *testing.T) {
	h := newHarness(t, map[string]string{"empty.txt": ""})
	h.owners.Catalog = sourcecatalog.New()
	for range 2 {
		result := h.exec("", "grep", "-r", "needle", ".")
		result.expect(t, "empty input", "", "", 1)
		if len(h.pins.paths(result.execution)) != 1 {
			t.Fatal("zero-size input was excluded without a live capture")
		}
	}
}
