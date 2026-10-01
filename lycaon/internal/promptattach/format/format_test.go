package format_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDetectContainers(t *testing.T) {
	cases := []struct {
		name  string
		magic []byte
		want  format.Container
	}{
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00}, format.ContainerGzip},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00}, format.ContainerZstd},
		{"xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0x00}, format.ContainerXz},
		{"bzip2", []byte{'B', 'Z', 'h', '9'}, format.ContainerBzip2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := format.Detect("x."+tc.name, "", tc.magic)
			if got.Kind != format.KindContainer || got.Container != tc.want {
				t.Fatalf("Detect = %+v want container %s", got, tc.want)
			}
			if got.Terminal() {
				t.Fatal("a container must not report Terminal")
			}
		})
	}
}

// OOXML and ODF are zips. Detection claims them as documents, so the unwrap
// stage is never offered one.
func TestDetectClaimsOfficePackagesBeforeContainers(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	testutil.FailErr(t, "create zip member", err)
	_, err = w.Write([]byte("<w:document/>"))
	testutil.FailErr(t, "write zip member", err)
	testutil.FailErr(t, "close zip", zw.Close())

	got := format.Detect("report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", buf.Bytes())
	if got.Kind != format.KindDocument {
		t.Fatalf("Detect = %+v want document", got)
	}
	if !got.Terminal() {
		t.Fatal("a document must be terminal")
	}
}

// A bare zip is a tree, not one body: not a container, not admitted.
func TestDetectRejectsBareArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	testutil.FailErr(t, "close zip", zw.Close())
	got := format.Detect("bundle.zip", "application/zip", buf.Bytes())
	if got.Kind != format.KindUnsupported {
		t.Fatalf("Detect = %+v want unsupported", got)
	}
}

// http.DetectContentType calls JSON, YAML, TOML and prose all text/plain. The
// suffix is the only discriminator, and it decides jq vs byte paging.
func TestDetectRefinesTextTypeFromSuffix(t *testing.T) {
	body := []byte(`{"a":1}`)
	for name, want := range map[string]string{
		"trace.json": "application/json",
		"conf.yaml":  "application/yaml",
		"conf.yml":   "application/yaml",
		"conf.toml":  "application/toml",
		"notes.md":   "text/markdown",
		"data.csv":   "text/csv",
	} {
		got := format.Detect(name, "", body)
		if got.Kind != format.KindText || got.MIME != want {
			t.Fatalf("Detect(%q) = %+v want text/%s", name, got, want)
		}
	}
	if !format.IsStructuredQueryMIME("application/json") {
		t.Fatal("json must be queryable with jq")
	}
	if format.IsStructuredQueryMIME("text/markdown") {
		t.Fatal("markdown has no query tool and must page instead")
	}
}

func TestDetectRejectsBinaryClaimingText(t *testing.T) {
	got := format.Detect("evil.txt", "text/plain", []byte{0x00, 0x01, 0xff, 0xfe})
	if got.Kind != format.KindUnsupported {
		t.Fatalf("Detect = %+v want unsupported", got)
	}
}

func TestDetectRejectsExternalReferenceMIME(t *testing.T) {
	for _, mime := range []string{"text/uri-list", "message/external-body"} {
		got := format.Detect("remote.url", mime, []byte("https://example.invalid/file"))
		if got.Kind != format.KindUnsupported {
			t.Fatalf("Detect(%q) = %+v want unsupported", mime, got)
		}
	}
}

func TestDetectRequiresDocumentFamilyMagic(t *testing.T) {
	for name, mime := range map[string]string{
		"bad.pdf":  "application/pdf",
		"bad.rtf":  "application/rtf",
		"bad.docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	} {
		got := format.Detect(name, mime, []byte("not actually a document"))
		if got.Kind != format.KindUnsupported {
			t.Fatalf("Detect(%q, %q) = %+v want unsupported", name, mime, got)
		}
	}
}

func TestDetectRasterFromStoredBytes(t *testing.T) {
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	}
	for _, reported := range []string{"", "application/octet-stream", "image/jpeg"} {
		got := format.Detect("fixture.png", reported, png)
		if got.Kind != format.KindImage || got.MIME != "image/png" {
			t.Fatalf("Detect(reported=%q) = %+v want image/png", reported, got)
		}
	}
	if got := format.Detect("fake.png", "image/png", []byte{0x00, 0x01, 0xff, 0xfe}); got.Kind != format.KindUnsupported {
		t.Fatalf("claimed raster without raster bytes = %+v want unsupported", got)
	}
}

// Active markup never reaches the raster path; as UTF-8 source it is still text.
func TestDetectActiveMarkupNeverRoutesToImage(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`)
	got := format.Detect("logo.svg", "image/svg+xml", svg)
	if got.Kind != format.KindText || got.MIME != "image/svg+xml" {
		t.Fatalf("Detect = %+v want text/image-svg", got)
	}
	if got := format.Detect("logo.png", "image/png", svg); got.Kind == format.KindImage {
		t.Fatal("markup mislabeled image/png must not route to the raster path")
	}
}

// A multi-byte rune straddling the peek window is not evidence of binary.
func TestDetectTextProbeToleratesSplitRune(t *testing.T) {
	body := append(bytes.Repeat([]byte("a"), 8), []byte("é")[0])
	if got := format.Detect("notes.txt", "text/plain", body); got.Kind != format.KindText {
		t.Fatalf("Detect = %+v want text", got)
	}
}

func TestContainerInnerName(t *testing.T) {
	for in, want := range map[string]string{
		"recording.json.gz": "recording.json",
		"dump.sql.zst":      "dump.sql",
		"log.txt.xz":        "log.txt",
		"data.csv.bz2":      "data.csv",
		// Tree archives retain their suffix for rejection.
		"bundle.tgz": "bundle.tgz",
	} {
		if got := format.ContainerGzip.InnerName(in); got != want {
			t.Fatalf("InnerName(%q) = %q want %q", in, got, want)
		}
	}
}

func TestContainerDecodeRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte("payload"))
	testutil.FailErr(t, "write gzip", err)
	testutil.FailErr(t, "close gzip", zw.Close())

	rc, err := format.ContainerGzip.Decode(bytes.NewReader(buf.Bytes()))
	testutil.FailErr(t, "decode gzip", err)
	defer func() { _ = rc.Close() }()
	var out bytes.Buffer
	_, err = out.ReadFrom(rc)
	testutil.FailErr(t, "read decoded", err)
	if out.String() != "payload" {
		t.Fatalf("decoded = %q want payload", out.String())
	}
}

func TestDetectVideoContainersFromStoredBytes(t *testing.T) {
	box := func(kind, brand string) []byte {
		return append([]byte("\x00\x00\x00\x18"+kind+brand), make([]byte, 32)...)
	}
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0x82, 0x84}, []byte("webm")...)
	cases := []struct {
		name string
		peek []byte
		want string
	}{
		{"screen.mp4", box("ftyp", "isom"), "video/mp4"},
		{"screen.mov", box("ftyp", "qt  "), "video/quicktime"},
		{"old.mov", box("moov", "\x00\x00\x00\x00"), "video/quicktime"},
		{"browser.webm", webm, "video/webm"},
	}
	for _, c := range cases {
		if got := format.Detect(c.name, "", c.peek); got.Kind != format.KindVideo || got.MIME != c.want {
			t.Fatalf("Detect(%s) = %+v want video %s", c.name, got, c.want)
		}
	}
	// Still images and audio share the ftyp box; they are not videos.
	for _, brand := range []string{"avif", "heic", "M4A "} {
		if got := format.Detect("still", "", box("ftyp", brand)); got.Kind == format.KindVideo {
			t.Fatalf("brand %q detected as video", brand)
		}
	}
	if !format.IsVideoMIME("video/webm") || format.IsVideoMIME("image/png") {
		t.Fatal("IsVideoMIME")
	}
}
