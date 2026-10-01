package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestObserveProjectSourceRetainsBoundedNonEditableRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}

	for _, tc := range []struct {
		name      string
		path      string
		content   []byte
		wantError error
	}{
		{name: "binary", path: "blob.bin", content: []byte("hello\x00world")},
		{name: "image", path: "pic.png", content: []byte{
			0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
			0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		}},
		{name: "unsupported encoding", path: "latin1.txt", content: []byte("caf\xe9\n"), wantError: ErrSourceUnsupportedEncoding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, tc.path), tc.content, 0o644))
			observation, err := ObserveProjectSource(p, SourceReadRequest{Path: tc.path})
			testutil.FailErr(t, "observe source", err)
			got, err := observation.Project()
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
			if !bytes.Equal(observation.Revision.Bytes, tc.content) {
				t.Fatalf("revision bytes = %x, want %x", observation.Revision.Bytes, tc.content)
			}
			if observation.Revision.SHA256 != textfile.SHA256(tc.content) {
				t.Fatalf("revision sha = %q", observation.Revision.SHA256)
			}
			if tc.wantError != nil {
				if got != nil {
					t.Fatal("refused source produced an editor projection")
				}
				return
			}
			if got == nil {
				t.Fatal("non-editable source has no metadata projection")
			}
			if got.SHA256 != "" {
				t.Fatalf("non-editable editor sha = %q, want empty", got.SHA256)
			}
		})
	}
}
