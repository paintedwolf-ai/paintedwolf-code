package modelstore

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func pinnedBundle(t *testing.T, signed SignedManifest) (ModelPin, []byte) {
	t.Helper()
	raw, err := json.Marshal(signed)
	testutil.FailErr(t, "encode manifest", err)
	m := signed.Manifest
	return ModelPin{m.Model, m.Device, m.Revision, fmt.Sprintf("%x", sha256.Sum256(raw)), manifestSize(m)}, raw
}

func TestPinnedDownloadResumesAndPurges(t *testing.T) {
	s, _, signed := bundleFixture(t)
	pin, raw := pinnedBundle(t, signed)
	const payload = "weights"
	var tries int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "manifest.json":
			_, _ = w.Write(raw)
		case signed.Manifest.Artifact.Name:
			tries++
			if tries == 1 {
				w.Header().Set("Content-Length", "7")
				_, _ = w.Write([]byte(payload[:3]))
				return
			}
			if r.Header.Get("Range") != "bytes=3-" {
				t.Errorf("resume header=%s", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 3-6/7")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(payload[3:]))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	download := func() error { return s.download(t.Context(), srv.URL, srv.Client(), pin, nil) }
	if err := download(); err == nil {
		t.Fatal("short transport accepted")
	}
	testutil.FailErr(t, "resume", download())
	fresh := New(s.Root(), s.signer)
	got, err := fresh.Selection()
	testutil.FailErr(t, "recover selection", err)
	if got != (Selection{"selected", pin.Model, pin.Device, pin.Revision}) {
		t.Fatalf("selection %+v", got)
	}
	lease, err := s.AcquireLease(pin.Model, pin.Device, "runtime", 1)
	testutil.FailErr(t, "acquire live revision", err)
	if err := fresh.PurgeAll(); !errors.Is(err, ErrRevisionInUse) {
		t.Fatalf("purge while leased: %v", err)
	}
	lease.Release()
	testutil.FailErr(t, "purge", fresh.PurgeAll())
	testutil.FailErr(t, "repeat purge", s.PurgeAll())
	got, err = s.Selection()
	testutil.FailErr(t, "read opt-out", err)
	if got.State != "opted_out" || s.Verified(signed.Manifest.Artifact.Digest) {
		t.Fatalf("opt-out left %+v or payload", got)
	}
	if _, err := os.Stat(s.RevisionDir(signed.Manifest)); !os.IsNotExist(err) {
		t.Fatalf("purged revision: %v", err)
	}
}

func TestPinnedManifestRejectedBeforePayload(t *testing.T) {
	for _, mode := range []string{"digest", "bytes", "key", "model", "revision", "device", "unknown", "oversize", "http", "pin"} {
		t.Run(mode, func(t *testing.T) {
			s, _, signed := bundleFixture(t)
			if mode == "key" {
				signed.KeyID = "unknown"
			}
			pin, raw := pinnedBundle(t, signed)
			switch mode {
			case "digest":
				pin.ManifestSHA256 = strings.Repeat("0", 64)
			case "bytes":
				pin.TotalBytes++
			case "model":
				pin.Model = "other"
			case "revision":
				pin.Revision = "other"
			case "device":
				pin.Device = "other"
			case "unknown":
				raw = []byte("{}")
				pin.ManifestSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
			case "oversize":
				raw = make([]byte, maxBundleManifestBytes+1)
			case "pin":
				pin.Model = "../a"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if filepath.Base(r.URL.Path) != "manifest.json" {
					t.Errorf("untrusted payload request %v", r.URL)
				}
				if mode == "http" {
					w.WriteHeader(http.StatusForbidden)
				}
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			if err := s.download(t.Context(), srv.URL, srv.Client(), pin, nil); err == nil {
				t.Fatal("accepted invalid pin or manifest")
			}
		})
	}
}

func writeTarBundle(t *testing.T, dir string, names []string) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "model.pwmodel"))
	testutil.FailErr(t, "create archive", err)
	w := tar.NewWriter(f)
	for _, name := range names {
		raw := []byte("extra")
		if name == "manifest.json" || name == "model.safetensors" {
			raw, err = os.ReadFile(filepath.Join(dir, name))
			testutil.FailErr(t, "read member", err)
		}
		testutil.FailErr(t, "write header", w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(raw))}))
		_, err = w.Write(raw)
		testutil.FailErr(t, "write member", err)
	}
	testutil.FailErr(t, "finish tar", w.Close())
	testutil.FailErr(t, "finish file", f.Close())
	return f.Name()
}

func TestOfflineSelection(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(fmt.Sprint(archive), func(t *testing.T) {
			s, dir, signed := bundleFixture(t)
			path := dir
			if archive {
				path = writeTarBundle(t, dir, []string{"model.safetensors", "manifest.json"})
			}
			slot, err := BundleSlot(t.Context(), path)
			testutil.FailErr(t, "inspect", err)
			if slot.Model != signed.Manifest.Model {
				t.Fatalf("slot %+v", slot)
			}
			testutil.FailErr(t, "import old bundle", s.ImportBundle(t.Context(), dir, slot))
			selected, err := s.Selection()
			testutil.FailErr(t, "old selection", err)
			if selected.Model != slot.Model {
				t.Fatalf("old selection %+v", selected)
			}
			var done, total int64
			testutil.FailErr(t, "import bundle", s.ImportFromBundlePath(t.Context(), path, slot, func(d, n int64) { done, total = d, n }))
			if done != signed.Manifest.Artifact.Bytes || done != total || s.Status(slot.Model, slot.Device).Previous != nil {
				t.Fatalf("progress %d/%d", done, total)
			}
			if err := s.ImportBundle(t.Context(), dir, slot); err == nil {
				t.Fatal("seed overwrote explicit selection")
			}
			if err := s.ImportFromBundlePath(t.Context(), path, Slot{}, nil); err == nil {
				t.Fatal("accepted wrong tier")
			}
		})
	}
}

func TestRejectUnsafeArchives(t *testing.T) {
	s, dir, _ := bundleFixture(t)
	for _, names := range [][]string{{"manifest.json", "manifest.json"}, {"../model"}, {"sub/model"}, {".manifest"}, {"model.safetensors"}} {
		p := writeTarBundle(t, dir, names)
		if err := s.ImportFromBundlePath(t.Context(), p, Slot{"e4b", "mlx"}, nil); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := BundleSlot(ctx, writeTarBundle(t, dir, []string{"manifest.json"})); !errors.Is(err, context.Canceled) {
		t.Fatalf("inspect canceled archive: %v", err)
	}
}
