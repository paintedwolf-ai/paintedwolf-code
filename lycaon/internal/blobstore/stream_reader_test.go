package blobstore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStreamReaderRejectsOversizedHistoryWithoutChangingGenericReader(t *testing.T) {
	store := Store{Root: t.TempDir(), Dir: "fixture-streams"}
	blob, err := store.Put("fixture.bin", bytes.NewBufferString("fixture"), bytebound.Materialization(100))
	testutil.FailErr(t, "stage fixture", err)
	// Zstandard frame with a16MiB declared history and a final empty raw block.
	// Its exact frame bytes exercise the decoder door independently of stdout.
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0x70, 1, 0, 0}
	testutil.FailErr(t, "replace fixture frame", os.WriteFile(filepath.Join(store.Root, filepath.FromSlash(blob.Rel)), frame, 0600))
	bounded, err := store.OpenStream(blob)
	if err == nil {
		_, err = io.ReadAll(bounded)
		testutil.FailErr(t, "close refused stream decoder", bounded.Close())
	}
	if !errors.Is(err, zstd.ErrWindowSizeExceeded) && !errors.Is(err, zstd.ErrDecoderSizeExceeded) {
		t.Fatalf("large window escaped selected decoder profile: %v", err)
	}
	generic, err := store.Open(blob)
	testutil.FailErr(t, "retain generic reader profile", err)
	got, err := io.ReadAll(generic)
	testutil.FailErr(t, "decode generic accepted frame", err)
	testutil.FailErr(t, "close generic reader", generic.Close())
	if len(got) != 0 {
		t.Fatal("empty fixture frame produced bytes")
	}
}
func TestStreamReaderPreservesExistingRecompressedBytes(t *testing.T) {
	store := Store{Root: t.TempDir(), Dir: "fixture-streams"}
	want := bytes.Repeat([]byte("fixture bytes "), 20000)
	blob, err := store.Put("fixture.bin", bytes.NewReader(want), bytebound.Materialization(len(want)))
	testutil.FailErr(t, "stage existing encoding", err)
	_, err = store.Recompress(blob)
	testutil.FailErr(t, "use existing recompression", err)
	reader, err := store.OpenStream(blob)
	testutil.FailErr(t, "open bounded recompressed body", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read bounded body", err)
	testutil.FailErr(t, "close decoder", reader.Close())
	if !bytes.Equal(got, want) {
		t.Fatal("bounded reader changed actual body bytes")
	}
}

func TestBoundedStreamReaderDoesNotCapCompleteBodyAtHistoryLimit(t *testing.T) {
	store := Store{Root: t.TempDir(), Dir: "fixture-streams"}
	want := bytes.Repeat([]byte("fixture complete body bytes "), 400000)
	blob, err := store.Put("large-fixture.bin", bytes.NewReader(want), bytebound.Materialization(len(want)))
	testutil.FailErr(t, "stage body larger than decoder history", err)
	_, err = store.Recompress(blob)
	testutil.FailErr(t, "recompress large existing body", err)
	reader, err := store.OpenStream(blob)
	testutil.FailErr(t, "open large bounded stream", err)
	digest := sha256.New()
	n, err := io.Copy(digest, reader)
	testutil.FailErr(t, "stream complete large body", err)
	testutil.FailErr(t, "close large decoder", reader.Close())
	expected := sha256.Sum256(want)
	if n != int64(len(want)) || !bytes.Equal(digest.Sum(nil), expected[:]) {
		t.Fatal("history ceiling truncated complete body")
	}
}
