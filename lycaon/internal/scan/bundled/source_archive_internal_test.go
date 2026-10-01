package bundled

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceArchiveRejectsUnsafeMemberKindsAndPaths(t *testing.T) {
	cases := []struct {
		name    string
		members []*tar.Header
		valid   bool
	}{
		{"dangling relative symlink", []*tar.Header{{Name: "engine/link", Typeflag: tar.TypeSymlink, Linkname: "../grammars/dangling"}}, true},
		{"empty regular source", []*tar.Header{{Name: "engine/empty", Typeflag: tar.TypeReg}}, true},
		{"absolute symlink", []*tar.Header{{Name: "engine/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/elsewhere"}}, false},
		{"escaping symlink", []*tar.Header{{Name: "engine/link", Typeflag: tar.TypeSymlink, Linkname: "../../elsewhere"}}, false},
		{"hardlink", []*tar.Header{{Name: "engine/link", Typeflag: tar.TypeLink, Linkname: "engine/file"}}, false},
		{"directory", []*tar.Header{{Name: "engine/dir", Typeflag: tar.TypeDir}}, false},
		{"traversing member", []*tar.Header{{Name: "engine/../elsewhere", Typeflag: tar.TypeReg}}, false},
		{"duplicate member", []*tar.Header{{Name: "engine/file", Typeflag: tar.TypeReg}, {Name: "engine/file", Typeflag: tar.TypeReg}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			compressed := gzip.NewWriter(&buffer)
			writer := tar.NewWriter(compressed)
			for _, header := range tc.members {
				testutil.FailErr(t, "write archive member", writer.WriteHeader(header))
			}
			testutil.FailErr(t, "write manifest header", writer.WriteHeader(&tar.Header{Name: "SOURCE-MANIFEST.json", Typeflag: tar.TypeReg, Size: 2}))
			_, err := writer.Write([]byte("{}"))
			testutil.FailErr(t, "write manifest bytes", err)
			testutil.FailErr(t, "close tar", writer.Close())
			testutil.FailErr(t, "close gzip", compressed.Close())
			filename := filepath.Join(t.TempDir(), "source.tar.gz")
			testutil.FailErr(t, "write archive fixture", os.WriteFile(filename, buffer.Bytes(), 0o600))
			_, _, err = readSourceArchive(filename)
			if (err == nil) != tc.valid {
				t.Fatalf("archive accepted=%v, want%v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
