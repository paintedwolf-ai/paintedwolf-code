package visual

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordedMediaStoragePreservesOriginalBytes(t *testing.T) {
	for _, name := range []string{"static", "typing", "scroll"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "recordings", name+".mp4"))
			testutil.FailErr(t, "read controlled browser recording", err)
			f := newDurableFixture(t, []string{"session"})
			wire, err := f.store.Put(t.Context(), "session", Entry{
				Meta: api.VisualArtifact{Mime: "video/mp4", Source: api.VisualArtifactSourceCapture}, Bytes: raw, AutomaticRecording: true,
			})
			testutil.FailErr(t, "store lossless recording", err)
			record := f.record(t, wire.ID)
			encoded, err := os.ReadFile(filepath.Join(f.blobDir(t), record.ContentHash))
			testutil.FailErr(t, "read compressed recording", err)
			if record.ByteSize != int64(len(raw)) || record.StoredSize != int64(len(encoded)) || len(encoded) >= len(raw) {
				t.Fatalf("storage sizes: raw=%d encoded=%d record=%+v", len(raw), len(encoded), record)
			}
			restored, ok := readArtifactBlob(f.blobDir(t), record.ContentHash, record.ByteSize)
			if !ok || !bytes.Equal(restored, raw) {
				t.Fatal("recording bytes changed across compressed storage")
			}
			testutil.FailErr(t, "stream verify stored recording", VerifyArtifactBody(bytes.NewReader(encoded), record.ContentHash, record.ByteSize))
			var gz bytes.Buffer
			encoder, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
			testutil.FailErr(t, "create comparison codec", err)
			_, err = encoder.Write(raw)
			testutil.FailErr(t, "compress comparison", err)
			testutil.FailErr(t, "finish comparison", encoder.Close())
			t.Logf("recording=%s plaintext=%d zstd_default=%d gzip_best=%d zstd_savings=%.2f%%", name, len(raw), len(encoded), gz.Len(), 100*(1-float64(len(encoded))/float64(len(raw))))
			for _, check := range []struct {
				name string
				body []byte
				hash string
				size int64
			}{
				{"raw media", raw, record.ContentHash, record.ByteSize},
				{"oversized claim", nil, record.ContentHash, MaxDurableBodyBytes + 1},
				{"truncated stream", encoded[:len(encoded)-1], record.ContentHash, record.ByteSize},
				{"wrong hash", encoded, artifactContentHash([]byte("other")), record.ByteSize},
				{"excess decoded bytes", encoded, record.ContentHash, record.ByteSize - 1},
				{"missing decoded bytes", encoded, record.ContentHash, record.ByteSize + 1},
				{"trailing garbage", append(bytes.Clone(encoded), 1), record.ContentHash, record.ByteSize},
			} {
				if err := VerifyArtifactBody(bytes.NewReader(check.body), check.hash, check.size); err == nil {
					t.Errorf("accepted %s", check.name)
				}
			}
		})
	}
}

func TestLowerUploadLimitKeepsRetainedVisualReadable(t *testing.T) {
	f := newDurableFixture(t, []string{"session"})
	raw := onePixelPNG(t)
	wire, err := f.store.Put(t.Context(), "session", Entry{Meta: api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceCapture}, Bytes: raw})
	testutil.FailErr(t, "publish original artifact", err)
	caps := promptattach.Active()
	t.Cleanup(func() { promptattach.Install(caps) })
	lower := caps
	lower.Transport.MaxImage = bytebound.Transport(1)
	promptattach.Install(lower)
	result := f.restart().Resolve(t.Context(), "session", wire.ID)
	if !result.IsPresent() || !bytes.Equal(result.Bytes(), raw) {
		t.Fatal("lower new-upload cap invalidated retained body")
	}
	if _, _, err := prepareEntry(Entry{Meta: api.VisualArtifact{Mime: "image/png"}, Bytes: raw}); err == nil {
		t.Fatal("new upload ignored lowered cap")
	}
}
