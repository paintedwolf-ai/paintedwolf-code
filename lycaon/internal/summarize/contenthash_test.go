package summarize

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHashBytesStable(t *testing.T) {
	b := []byte("pack leaf body")
	sum := sha256.Sum256(b)
	want := hex.EncodeToString(sum[:])
	if got := hashBytes(b); got != want {
		t.Fatalf("hashBytes: got %q want %q", got, want)
	}
}

func TestHashStringStable(t *testing.T) {
	if got := HashString("inline"); got != hashBytes([]byte("inline")) {
		t.Fatal("HashString mismatch with byte hashing")
	}
}

func TestLeafKinds(t *testing.T) {
	if KindFile != "file" || KindInline != "inline" {
		t.Fatalf("leaf kind constants: file=%q inline=%q", KindFile, KindInline)
	}
}
