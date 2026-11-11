package contactsheet

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/png"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func filmstripFixture(t *testing.T, manifest any, frame []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	metadata, err := json.Marshal(manifest)
	testutil.FailErr(t, "marshal fixture", err)
	for name, raw := range map[string][]byte{"manifest.json": metadata, "frame.png": frame} {
		entry, err := writer.Create(name)
		testutil.FailErr(t, "create entry", err)
		_, err = entry.Write(raw)
		testutil.FailErr(t, "write entry", err)
	}
	testutil.FailErr(t, "finish archive", writer.Close())
	return archive.Bytes()
}

func TestFilmstripRejectsResourceExpansionBeforeImageDecode(t *testing.T) {
	var encoded bytes.Buffer
	testutil.FailErr(t, "encode tiny frame", png.Encode(&encoded, frame(1, 1)))
	huge := append([]byte(nil), encoded.Bytes()...)
	binary.BigEndian.PutUint32(huge[16:20], 1<<24)
	binary.BigEndian.PutUint32(huge[20:24], 1<<24)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	one := []map[string]any{{"file": "frame.png", "index": 0}}
	if _, err := FilmstripPoster(filmstripFixture(t, map[string]any{"frames": one}, huge), 2048); err == nil {
		t.Fatal("accepted frame whose dimensions exceed the allocation bound")
	}
	many := make([]map[string]any, maxFilmstripFrames+1)
	for i := range many {
		many[i] = map[string]any{"file": "frame.png", "index": i}
	}
	if _, err := FilmstripPoster(filmstripFixture(t, map[string]any{"frames": many}, encoded.Bytes()), 2048); err == nil {
		t.Fatal("accepted manifest beyond frame count bound")
	}
	for _, name := range []string{"poster.png", "manifest.json", "frame.png"} {
		entry := &zip.File{FileHeader: zip.FileHeader{Name: name, UncompressedSize64: maxFilmstripEntryBytes + 1}}
		if _, err := readZipEntry(entry); err == nil {
			t.Errorf("accepted oversized %s before opening it", name)
		}
	}
}

func TestFilmstripPixelReservationsStayWithinBudget(t *testing.T) {
	for _, width := range []int{-1, 0, 1, 4096, 4097, int(^uint(0) >> 1)} {
		for _, height := range []int{-1, 0, 1, 4096, 4097, int(^uint(0) >> 1)} {
			for _, budget := range []int64{0, 1, maxFilmstripFramePixels, maxFilmstripTotalPixels} {
				left, err := reserveFilmstripPixels(image.Config{Width: width, Height: height}, budget)
				fits := width > 0 && height > 0 && int64(width) <= int64(maxFilmstripFramePixels)/int64(height)
				if fits {
					fits = int64(width)*int64(height) <= budget
				}
				if (err == nil) != fits {
					t.Fatalf("%dx%d with %d pixels: left=%d err=%v", width, height, budget, left, err)
				}
				if err != nil && left != budget || err == nil && (left < 0 || left != budget-int64(width)*int64(height)) {
					t.Fatalf("invalid pixel reservation: left=%d budget=%d err=%v", left, budget, err)
				}
			}
		}
	}
}
