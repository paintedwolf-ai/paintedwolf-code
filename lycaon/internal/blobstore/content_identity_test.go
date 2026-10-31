package blobstore_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBlobMatchesPlaintextDigestWithItsOwnAddressingRule(t *testing.T) {
	store := newStore(t)
	blob, err := store.Put("output.bin", strings.NewReader("exact bytes"), bytebound.Materialization(128))
	testutil.FailErr(t, "retain content", err)
	digest := sha256.Sum256([]byte("exact bytes"))
	plain := hex.EncodeToString(digest[:])
	if blob.ID == plain || !blob.MatchesContentDigest(plain) || blob.MatchesContentDigest(blob.ID) || blob.MatchesContentDigest("invalid") {
		t.Fatalf("content identity confused with blob address: %+v", blob)
	}
	renamed := blob
	renamed.Name = "other.bin"
	if renamed.MatchesContentDigest(plain) {
		t.Fatal("renamed identity matched original digest")
	}
}
