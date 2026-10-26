package projectroot

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSessionAddress(t *testing.T) {
	cases := []struct {
		raw     string
		wantRel string
		wantOK  bool
	}{
		{raw: "@scratch", wantRel: "", wantOK: true},
		{raw: "@scratch/", wantRel: "", wantOK: true},
		{raw: "@scratch/notes.md", wantRel: "notes.md", wantOK: true},
		{raw: "  @Scratch/a/b.txt ", wantOK: false},
		{raw: "@Scratch/a/b.txt ", wantRel: "a/b.txt ", wantOK: true},
		{raw: "@SCRATCH/../x", wantRel: "../x", wantOK: true},
		{raw: "@scratchpad/x", wantOK: false},
		{raw: "@ scratch/x", wantOK: false},
		{raw: "@docs/x", wantOK: false},
		{raw: "scratch/x", wantOK: false},
		{raw: "/tmp/scratch", wantOK: false},
		{raw: "", wantOK: false},
	}
	for _, tc := range cases {
		area, rel, ok := SessionAddress(tc.raw)
		if ok && area != "scratch" {
			t.Fatalf("unexpected session area %q", area)
		}
		if ok != tc.wantOK || rel != tc.wantRel {
			t.Errorf("SessionAddress(%q) = (%q, %v), want (%q, %v)", tc.raw, rel, ok, tc.wantRel, tc.wantOK)
		}
	}
}

func TestSessionPath(t *testing.T) {
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
		got, err := SessionPath(dir, tc.rel)
		if tc.escapes {
			if !errors.Is(err, ErrPathEscape) {
				t.Errorf("SessionPath(%q) err = %v, want ErrPathEscape", tc.rel, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("SessionPath(%q) = (%q, %v), want %q", tc.rel, got, err, tc.want)
		}
	}
	if _, err := SessionPath("relative/dir", "x"); err == nil {
		t.Error("SessionPath with a relative folder: want error")
	}
}

func TestOutputNamespaceIsReservedAndPreservesNames(t *testing.T) {
	for _, raw := range []string{"@output/a.stdout", "@OUTPUT/a.stdout"} {
		area, rel, ok := SessionAddress(raw)
		if !ok || area != VirtualOutputLabel || rel != "a.stdout" {
			t.Fatalf("address=%q/%q/%v", area, rel, ok)
		}
	}
	if !IsVirtualRootLabel("output") {
		t.Fatal("output could shadow a host namespace")
	}
}
