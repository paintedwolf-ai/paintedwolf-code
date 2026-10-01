package projectroot

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestScratchAddress(t *testing.T) {
	cases := []struct {
		raw     string
		wantRel string
		wantOK  bool
	}{
		{raw: "@scratch", wantRel: "", wantOK: true},
		{raw: "@scratch/", wantRel: "", wantOK: true},
		{raw: "@scratch/notes.md", wantRel: "notes.md", wantOK: true},
		{raw: "  @Scratch/a/b.txt ", wantRel: "a/b.txt", wantOK: true},
		{raw: "@SCRATCH/../x", wantRel: "../x", wantOK: true},
		{raw: "@scratchpad/x", wantOK: false},
		{raw: "@ scratch/x", wantOK: false},
		{raw: "@docs/x", wantOK: false},
		{raw: "scratch/x", wantOK: false},
		{raw: "/tmp/scratch", wantOK: false},
		{raw: "", wantOK: false},
	}
	for _, tc := range cases {
		rel, ok := ScratchAddress(tc.raw)
		if ok != tc.wantOK || rel != tc.wantRel {
			t.Errorf("ScratchAddress(%q) = (%q, %v), want (%q, %v)", tc.raw, rel, ok, tc.wantRel, tc.wantOK)
		}
	}
}

func TestScratchPath(t *testing.T) {
	dir := filepath.FromSlash("/state/scratch/chat-1")
	cases := []struct {
		rel     string
		want    string
		escapes bool
	}{
		{rel: "", want: dir},
		{rel: ".", want: dir},
		{rel: "notes.md", want: filepath.Join(dir, "notes.md")},
		{rel: "a/./b/../c.txt", want: filepath.Join(dir, "a", "c.txt")},
		{rel: "..", escapes: true},
		{rel: "../chat-2/x", escapes: true},
		{rel: "a/../../x", escapes: true},
		{rel: "/etc/passwd", escapes: true},
	}
	for _, tc := range cases {
		got, err := ScratchPath(dir, tc.rel)
		if tc.escapes {
			if !errors.Is(err, ErrPathEscape) {
				t.Errorf("ScratchPath(%q) err = %v, want ErrPathEscape", tc.rel, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ScratchPath(%q) = (%q, %v), want %q", tc.rel, got, err, tc.want)
		}
	}
	if _, err := ScratchPath("relative/dir", "x"); err == nil {
		t.Error("ScratchPath with a relative folder: want error")
	}
}
