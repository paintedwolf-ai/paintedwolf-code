package sourceview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// TestDocumentBytesEncodeTheRecordedRepresentation: accepted text is served
// exactly as its save writes it, and a clean document that does not round
// trip is served from the published bytes it accepted.
func TestDocumentBytesEncodeTheRecordedRepresentation(t *testing.T) {
	root := t.TempDir()
	mixed := "one\r\ntwo\nthree\r\n"
	testutil.FailErr(t, "seed mixed", os.WriteFile(filepath.Join(root, "mixed.txt"), []byte(mixed), 0o644))
	resolved := projectpaths.Resolved{Abs: filepath.Join(root, "mixed.txt"), DisplayPath: "mixed.txt", ScopeRel: "mixed.txt", Root: projectroot.RootRef{ID: "r1", Path: root}}
	crlf := "a\r\nb\r\n"
	utf16, err := textfile.EncodeBounded("hi\r\n", textfile.UTF16LEBOM, textfile.LimitsForRaw(64))
	testutil.FailErr(t, "encode utf16", err)
	for name, c := range map[string]struct {
		doc  tools.EditorDocumentText
		want string
	}{
		"dirty crlf":   {tools.EditorDocumentText{Text: "a\nb\n", EOL: "crlf", Encoding: textfile.UTF8, SHA256: textfile.SHA256([]byte(crlf)), Dirty: true}, crlf},
		"utf16 bom":    {tools.EditorDocumentText{Text: "hi\n", EOL: "crlf", Encoding: textfile.UTF16LEBOM, SHA256: textfile.SHA256(utf16), Dirty: true}, string(utf16)},
		"default utf8": {tools.EditorDocumentText{Text: "x\n", SHA256: textfile.SHA256([]byte("x\n")), Dirty: true}, "x\n"},
		"clean mixed":  {tools.EditorDocumentText{Text: "one\ntwo\nthree\n", EOL: "crlf", Encoding: textfile.UTF8, SHA256: textfile.SHA256([]byte(mixed))}, mixed},
	} {
		got, err := DocumentBytes(c.doc, resolved, 1<<20)
		if err != nil || string(got) != c.want {
			t.Errorf("%s = %q %v, want %q", name, got, err, c.want)
		}
	}
	if _, err := DocumentBytes(tools.EditorDocumentText{Text: "a\n", Encoding: textfile.UTF8, SHA256: "elsewhere", Dirty: true}, resolved, 1<<20); !errors.Is(err, ErrDocumentUnrepresentable) {
		t.Fatalf("mismatched draft = %v", err)
	}
	if _, err := DocumentBytes(tools.EditorDocumentText{Text: "a\n", Encoding: textfile.UTF8, SHA256: "elsewhere"}, resolved, 1<<20); !errors.Is(err, ErrDocumentUnrepresentable) {
		t.Fatalf("mismatched clean document = %v", err)
	}
	if _, err := DocumentBytes(tools.EditorDocumentText{Text: "toolong\n", Encoding: textfile.UTF8, SHA256: textfile.SHA256([]byte("toolong\n")), Dirty: true}, resolved, 4); err == nil {
		t.Fatal("over-budget document encoded")
	}
	if _, err := DocumentBytes(tools.EditorDocumentText{Text: "a\n", Encoding: "ebcdic", Dirty: true}, resolved, 64); !errors.Is(err, ErrDocumentUnrepresentable) || !errors.Is(err, textfile.ErrUnsupported) {
		t.Fatalf("unknown encoding = %v", err)
	}
}
