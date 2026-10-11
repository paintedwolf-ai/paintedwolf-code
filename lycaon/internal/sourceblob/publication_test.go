package sourceblob

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestPublicationRepairsDamagedObjects(t *testing.T) {
	for _, capture := range []string{"memory", "file"} {
		for _, damage := range []string{"truncated", "same_size", "wrong_content"} {
			t.Run(capture+"/"+damage, func(t *testing.T) {
				store := New(t.TempDir())
				body := []byte("retained source history\n")
				sha := ContentSHA(body)
				rel, _, _, err := store.Put(sha, body)
				testutil.FailErr(t, "seed object", err)
				path := filepath.Join(store.Root(), rel)
				encoded, err := os.ReadFile(path)
				testutil.FailErr(t, "read object", err)
				switch damage {
				case "truncated":
					encoded = encoded[:len(encoded)/2]
				case "same_size":
					encoded[len(encoded)-1] ^= 0xff
				case "wrong_content":
					encoded, err = zstdcodec.Compress(bytes.NewReader([]byte("different source history\n")))
					testutil.FailErr(t, "encode different content", err)
				}
				testutil.FailErr(t, "allow damage injection", os.Chmod(path, 0o600))
				testutil.FailErr(t, "damage object", os.WriteFile(path, encoded, 0o600))
				testutil.FailErr(t, "protect damaged object", os.Chmod(path, 0o400))
				publishObject(t, store, capture, sha, body)
				var retained bytes.Buffer
				testutil.FailErr(t, "read verified history", store.CopySHA(t.Context(), sha, &retained))
				if !bytes.Equal(retained.Bytes(), body) {
					t.Fatal("publication retained different content")
				}
				before, err := os.Stat(path)
				testutil.FailErr(t, "stat repaired object", err)
				publishObject(t, store, capture, sha, body)
				after, err := os.Stat(path)
				testutil.FailErr(t, "stat reused object", err)
				if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("publication replaced a valid object")
				}
			})
		}
	}
}

func publishObject(t *testing.T, store *Store, capture, sha string, body []byte) {
	t.Helper()
	var rel string
	var stored int64
	if capture == "memory" {
		var err error
		rel, stored, _, err = store.Put(sha, body)
		testutil.FailErr(t, "publish memory content", err)
	} else {
		path := filepath.Join(t.TempDir(), "source")
		testutil.FailErr(t, "write capture source", os.WriteFile(path, body, 0o600))
		result, err := store.PutFile(t.Context(), path)
		testutil.FailErr(t, "publish file content", err)
		rel, stored = result.Rel, result.Stored
		if result.SHA256 != sha {
			t.Fatal("capture changed content identity")
		}
	}
	info, err := os.Stat(filepath.Join(store.Root(), rel))
	testutil.FailErr(t, "stat published object", err)
	if stored != info.Size() {
		t.Fatal("publication reported the wrong stored size")
	}
}

func TestPublicationRefusesInvalidDestinations(t *testing.T) {
	body := []byte("retained")
	sha := ContentSHA(body)
	var absent *Store
	if _, _, _, err := absent.Put(sha, body); err == nil {
		t.Fatal("nil store accepted content")
	}
	store := New(t.TempDir())
	rel, err := RelPath(sha)
	testutil.FailErr(t, "derive object path", err)
	testutil.FailErr(t, "occupy object path", os.MkdirAll(filepath.Join(store.Root(), rel), 0o700))
	if _, _, _, err := store.Put(sha, body); err == nil {
		t.Fatal("directory accepted as retained content")
	}
}

func TestObjectVerificationHonorsCancellation(t *testing.T) {
	store := New(t.TempDir())
	body := []byte("retained")
	rel, _, _, err := store.Put(ContentSHA(body), body)
	testutil.FailErr(t, "publish object", err)
	encoded, err := os.ReadFile(filepath.Join(store.Root(), rel))
	testutil.FailErr(t, "read encoded object", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if matched, err := store.matchesObject(ctx, rel, int64(len(encoded)), ContentSHA(encoded)); err == nil || matched {
		t.Fatal("canceled verification accepted an object")
	}
	testutil.FailErr(t, "retained object remains readable", store.CopySHA(t.Context(), ContentSHA(body), io.Discard))
}
