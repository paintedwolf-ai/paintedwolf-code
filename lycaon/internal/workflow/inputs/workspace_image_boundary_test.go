package inputs

import (
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceImageReaderValidatesContentAndContainmentBeforeCapture(t *testing.T) {
	root := t.TempDir()
	reader := WorkspaceImageReaderForBoundary(nil, tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}})
	png := visual.TestPNG1x1Bytes()
	for _, tc := range []struct {
		name  string
		body  []byte
		mime  string
		valid bool
	}{
		{"preview.png", png, "image/png", true}, {"preview.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle/></svg>`), "image/svg+xml", true}, {"fake.png", []byte("not a png"), "", false}, {"broken.svg", []byte("<svg><g></svg>"), "", false}, {"document.txt", png, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, tc.name), tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			image, err := reader(t.Context(), tc.name)
			if (err == nil) != tc.valid {
				t.Fatalf("image=%+v err=%v", image, err)
			}
			if tc.valid && (image.DisplayPath != tc.name || image.Mime != tc.mime || len(image.Bytes) != len(tc.body)) {
				t.Fatalf("image changed=%+v", image)
			}
		})
	}
	outside := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.png")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"linked.png", "missing.png", "../private.png", "."} {
		if _, err := reader(t.Context(), ref); err == nil {
			t.Fatalf("invalid capture accepted %q", ref)
		}
	}
}
