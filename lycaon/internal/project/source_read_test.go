package project

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestReadProjectSourceInsideJail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rel := filepath.Join("src", "hello.go")
	abs := filepath.Join(root, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(abs, []byte("package main\n"), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "src/hello.go"})
	testutil.FailErr(t, "read", err)
	if got.Path != "src/hello.go" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.Content != "package main\n" {
		t.Fatalf("content = %q", got.Content)
	}
	if got.OverLimit || got.Binary {
		t.Fatalf("flags over=%v binary=%v", got.OverLimit, got.Binary)
	}
	if got.SizeBytes != int64(len("package main\n")) {
		t.Fatalf("size = %d", got.SizeBytes)
	}
}

func TestReadProjectSourceOutsideJail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	testutil.FailErr(t, "write outside", os.WriteFile(outside, []byte("nope"), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	_, err := ReadProjectSource(p, SourceReadRequest{Path: "../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt"})
	if !errors.Is(err, ErrSourcePathDenied) && !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err = %v want denied", err)
	}
	_, err = ReadProjectSource(p, SourceReadRequest{Path: outside})
	if !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("abs outside err = %v want denied", err)
	}
}

func TestReadProjectSourceMissingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	_, err := ReadProjectSource(p, SourceReadRequest{Path: "missing.ts"})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err = %v want not found", err)
	}
}

func TestReadProjectSourceOverLimitMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	abs := filepath.Join(root, "big.txt")
	payload := strings.Repeat("a", SourceReadMaxBytes+1)
	testutil.FailErr(t, "write", os.WriteFile(abs, []byte(payload), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "big.txt"})
	testutil.FailErr(t, "read", err)
	if !got.OverLimit {
		t.Fatal("want over_limit")
	}
	if got.Content != "" {
		t.Fatalf("content must be empty, got %d bytes", len(got.Content))
	}
	if got.SHA256 != "" {
		t.Fatal("sha must be empty for over-limit")
	}
	if got.SizeBytes != int64(len(payload)) {
		t.Fatalf("size = %d", got.SizeBytes)
	}
	if got.MIME == "" || got.MTime.IsZero() {
		t.Fatalf("want mime+mtime, got mime=%q mtime=%v", got.MIME, got.MTime)
	}
}

func TestReadProjectSourceJustUnderCeilingEditable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	abs := filepath.Join(root, "ok.txt")
	payload := strings.Repeat("b", SourceReadMaxBytes-1)
	testutil.FailErr(t, "write", os.WriteFile(abs, []byte(payload), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "ok.txt"})
	testutil.FailErr(t, "read", err)
	if got.OverLimit || got.Binary {
		t.Fatalf("flags = over=%v binary=%v", got.OverLimit, got.Binary)
	}
	if len(got.Content) != len(payload) {
		t.Fatalf("len = %d", len(got.Content))
	}
	if got.SHA256 == "" {
		t.Fatal("want sha256")
	}
}

func TestReadProjectSourceBinaryMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	abs := filepath.Join(root, "blob.bin")
	// A NUL without an encoding marker classifies as binary.
	testutil.FailErr(t, "write", os.WriteFile(abs, []byte("hello\x00world"), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      root,
			IsPrimary: true,
		}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "blob.bin"})
	testutil.FailErr(t, "read", err)
	if !got.Binary || got.Content != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestReadProjectSourceUTF8BOMAndWritable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	disk := testutil.EncodeTextFixture(t, "hello\n", textfile.UTF8BOM)
	abs := filepath.Join(root, "bom.txt")
	testutil.FailErr(t, "write", os.WriteFile(abs, disk, 0o644))
	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "bom.txt"})
	testutil.FailErr(t, "read", err)
	if got.Encoding != textfile.UTF8BOM || got.Content != "hello\n" {
		t.Fatalf("encoding=%q content=%q", got.Encoding, got.Content)
	}
	if got.SHA256 != textfile.SHA256(disk) {
		t.Fatalf("sha mismatch: %s", got.SHA256)
	}
	if !got.Writable {
		t.Fatal("0644 should be writable")
	}

	ro := filepath.Join(root, "ro.txt")
	testutil.FailErr(t, "write ro", os.WriteFile(ro, []byte("x\n"), 0o444))
	got, err = ReadProjectSource(p, SourceReadRequest{Path: "ro.txt"})
	testutil.FailErr(t, "read ro", err)
	if got.Writable {
		t.Fatal("0444 should not be writable")
	}
	if got.Encoding != textfile.UTF8 {
		t.Fatalf("encoding=%q", got.Encoding)
	}
}

func TestReadProjectSourceUnsupportedEncoding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}
	cases := []struct {
		name     string
		file     string
		data     []byte
		detected string
	}{
		{"cp1252", "legacy.txt", []byte("caf\xe9\n"), textfile.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, tc.file), tc.data, 0o644))
			_, err := ReadProjectSource(p, SourceReadRequest{Path: tc.file})
			if !errors.Is(err, ErrSourceUnsupportedEncoding) {
				t.Fatalf("err = %v", err)
			}
			var ue *SourceUnsupportedEncodingError
			if !errors.As(err, &ue) || ue.Detected != tc.detected {
				t.Fatalf("detected = %v want %s", err, tc.detected)
			}
		})
	}
}

func TestReadProjectSourceUTF16RoundTrips(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
	for _, encoding := range []string{
		textfile.UTF16LE,
		textfile.UTF16LEBOM,
		textfile.UTF16BE,
		textfile.UTF16BEBOM,
	} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			content := "hello 世界\n"
			disk := testutil.EncodeTextFixture(t, content, encoding)
			file := encoding + ".txt"
			path := filepath.Join(root, file)
			testutil.FailErr(t, "write", os.WriteFile(path, disk, 0o644))
			req := SourceReadRequest{Path: file}
			if encoding == textfile.UTF16LE || encoding == textfile.UTF16BE {
				req.DecodeAs = encoding
			}
			read, err := ReadProjectSource(p, req)
			testutil.FailErr(t, "read", err)
			if read.Encoding != encoding || read.Content != content {
				t.Fatalf("read encoding=%q content=%q", read.Encoding, read.Content)
			}
			_, err = writeProjectSource(p, SourceWriteRequest{
				Path: file, Content: read.Content, Encoding: read.Encoding, BaseSHA256: read.SHA256,
			})
			testutil.FailErr(t, "write unmodified", err)
			after, err := os.ReadFile(path)
			testutil.FailErr(t, "read after", err)
			if !bytes.Equal(after, disk) {
				t.Fatalf("byte drift: before=%x after=%x", disk, after)
			}
		})
	}
}

func TestReadProjectSourceRefusesBOMlessUTF16UntilExplicitlyReopened(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
	content := "hello 世界\n"
	for _, encoding := range []string{textfile.UTF16LE, textfile.UTF16BE} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			file := encoding + ".txt"
			disk := testutil.EncodeTextFixture(t, content, encoding)
			testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, file), disk, 0o644))
			read, err := ReadProjectSource(p, SourceReadRequest{Path: file})
			if err != nil || !read.Binary {
				t.Fatalf("automatic read = %#v err=%v, want binary metadata", read, err)
			}
			read, err = ReadProjectSource(p, SourceReadRequest{Path: file, DecodeAs: encoding})
			testutil.FailErr(t, "explicit read", err)
			if read.Encoding != encoding || read.Content != content {
				t.Fatalf("explicit read encoding=%q content=%q", read.Encoding, read.Content)
			}
		})
	}
}

func TestReadProjectSourceOpensControlsAcrossEncodings(t *testing.T) {
	t.Parallel()
	content := "before\x1b\f\u0085after\n"
	for _, encoding := range []string{
		textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LE,
		textfile.UTF16LEBOM, textfile.UTF16BE, textfile.UTF16BEBOM,
	} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			raw := testutil.EncodeTextFixture(t, content, encoding)
			testutil.FailErr(t, "write control fixture", os.WriteFile(filepath.Join(root, "controls.txt"), raw, 0o644))
			p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
			req := SourceReadRequest{Path: "controls.txt"}
			if encoding == textfile.UTF16LE || encoding == textfile.UTF16BE {
				req.DecodeAs = encoding
			}
			got, err := ReadProjectSource(p, req)
			testutil.FailErr(t, "read control fixture", err)
			if got.Binary || got.Encoding != encoding || got.Content != content {
				t.Fatalf("control read = %+v", got)
			}
		})
	}
}

func TestReadProjectSourceTreatsNULAsBinaryAcrossEncodings(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{
		textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LE,
		textfile.UTF16LEBOM, textfile.UTF16BE, textfile.UTF16BEBOM,
	} {
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			raw := testutil.EncodeTextFixture(t, "before\x00after", encoding)
			testutil.FailErr(t, "write NUL fixture", os.WriteFile(filepath.Join(root, "binary.txt"), raw, 0o644))
			p := &Project{ID: "p1", Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}}}
			req := SourceReadRequest{Path: "binary.txt"}
			if encoding == textfile.UTF16LE || encoding == textfile.UTF16BE {
				req.DecodeAs = encoding
			}
			got, err := ReadProjectSource(p, req)
			testutil.FailErr(t, "read NUL fixture", err)
			if !got.Binary || got.Content != "" || got.SHA256 != "" {
				t.Fatalf("NUL read = %+v", got)
			}
		})
	}
}

func TestReadProjectSourcePNGIsBinaryImage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Minimal PNG signature.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "pic.png"), png, 0o644))
	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}
	got, err := ReadProjectSource(p, SourceReadRequest{Path: "pic.png"})
	testutil.FailErr(t, "read", err)
	if !got.Binary || got.MIME != "image/png" {
		t.Fatalf("got binary=%v mime=%q", got.Binary, got.MIME)
	}
}

func TestReadProjectSourceRawImageAndRefusals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	testutil.FailErr(t, "png", os.WriteFile(filepath.Join(root, "a.png"), png, 0o644))
	// ELF masquerading as .png
	elf := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
	testutil.FailErr(t, "elf", os.WriteFile(filepath.Join(root, "fake.png"), elf, 0o644))
	testutil.FailErr(t, "txt", os.WriteFile(filepath.Join(root, "note.txt"), []byte("hi\n"), 0o644))

	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}
	raw, err := ReadProjectSourceRaw(p, SourceReadRequest{Path: "a.png"})
	testutil.FailErr(t, "raw png", err)
	if raw.ContentType != "image/png" || len(raw.Bytes) != len(png) {
		t.Fatalf("raw = %+v", raw)
	}
	_, err = ReadProjectSourceRaw(p, SourceReadRequest{Path: "fake.png"})
	if !errors.Is(err, ErrSourceRawNotImage) {
		t.Fatalf("fake png err = %v", err)
	}
	_, err = ReadProjectSourceRaw(p, SourceReadRequest{Path: "note.txt"})
	if !errors.Is(err, ErrSourceRawNotImage) {
		t.Fatalf("txt err = %v", err)
	}
}

func TestReadProjectSourceRawTooLarge(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	png := make([]byte, SourceRawMaxBytes+1)
	copy(png, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "huge.png"), png, 0o644))
	p := &Project{
		ID:    "p1",
		Roots: []Root{{ID: "r1", Path: root, IsPrimary: true}},
	}
	_, err := ReadProjectSourceRaw(p, SourceReadRequest{Path: "huge.png"})
	if !errors.Is(err, ErrSourceRawTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadProjectSourceEmptyPath(t *testing.T) {
	t.Parallel()
	p := &Project{
		ID: "p1",
		Roots: []Root{{
			ID:        "r1",
			Path:      t.TempDir(),
			IsPrimary: true,
		}},
	}
	_, err := ReadProjectSource(p, SourceReadRequest{Path: "  "})
	if !errors.Is(err, ErrSourcePathInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// Worker-created files are read from their overlay before promotion.
func TestReadProjectSourceRootIDPinsAmbiguousPath(t *testing.T) {
	t.Parallel()
	rootA := t.TempDir()
	rootB := t.TempDir()
	testutil.FailErr(t, "write A", os.WriteFile(filepath.Join(rootA, "README.md"), []byte("primary"), 0o644))
	testutil.FailErr(t, "write B", os.WriteFile(filepath.Join(rootB, "README.md"), []byte("secondary"), 0o644))

	p := &Project{
		ID: "p1",
		Roots: []Root{
			{ID: "ra", Path: rootA, Label: "a", IsPrimary: true},
			{ID: "rb", Path: rootB, Label: "b"},
		},
	}

	unpinned, err := ReadProjectSource(p, SourceReadRequest{Path: "README.md"})
	testutil.FailErr(t, "unpinned read", err)
	if unpinned.Content != "primary" {
		t.Fatalf("unpinned content = %q, want primary-first", unpinned.Content)
	}
	if unpinned.RootID != "ra" {
		t.Fatalf("unpinned RootID = %q, want serving root ra", unpinned.RootID)
	}

	pinned, err := ReadProjectSource(p, SourceReadRequest{Path: "README.md", RootID: "rb"})
	testutil.FailErr(t, "pinned read", err)
	if pinned.Content != "secondary" {
		t.Fatalf("pinned content = %q, want secondary root's file", pinned.Content)
	}
	if pinned.RootID != "rb" {
		t.Fatalf("pinned RootID = %q, want serving root rb", pinned.RootID)
	}

	if _, err := ReadProjectSource(p, SourceReadRequest{Path: "README.md", RootID: "r-unknown"}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("unknown root err = %v, want ErrSourceNotFound", err)
	}
}
