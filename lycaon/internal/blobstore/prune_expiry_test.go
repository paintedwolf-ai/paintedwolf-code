package blobstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPruneConsultsDurableClaimsOnlyForExpiredBodies(t *testing.T) {
	store := newStore(t)
	store.StagedTTL = time.Hour
	put := func(name string) blobstore.Blob {
		t.Helper()
		body, err := store.Put(name, strings.NewReader(name), bytebound.Materialization(100))
		testutil.FailErr(t, "put "+name, err)
		return body
	}
	recent := put("recent.txt")
	retained := put("retained.txt")
	unretained := put("unretained.txt")
	old := time.Now().Add(-2 * time.Hour)
	for _, body := range []blobstore.Blob{retained, unretained} {
		dir := filepath.Join(store.Root, "prompt-attachments", body.ID)
		testutil.FailErr(t, "age "+body.ID, os.Chtimes(dir, old, old))
	}
	calls := map[string]int{}
	store.Retained = func(id string) (bool, error) {
		calls[id]++
		return id == retained.ID, nil
	}
	testutil.FailErr(t, "prune expired bodies", store.Maintain())
	if calls[recent.ID] != 0 || calls[retained.ID] != 1 || calls[unretained.ID] != 1 {
		t.Fatalf("durable claim lookups = %v, want only the two expired bodies", calls)
	}
	for _, body := range []blobstore.Blob{recent, retained} {
		if _, err := store.Resolve(body.ID); err != nil {
			t.Fatalf("live body %s was collected: %v", body.ID, err)
		}
	}
	if _, err := store.Resolve(unretained.ID); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expired unretained body survived: %v", err)
	}
}
