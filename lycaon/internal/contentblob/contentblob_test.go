package contentblob_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteReadRoundTrip(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	plaintext := []byte("hello content-addressed world")

	sha, byteSize, storedSize, err := contentblob.Write(store, plaintext)
	testutil.FailErr(t, "write", err)

	wantSum := sha256.Sum256(plaintext)
	if sha != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("digest mismatch: got %s", sha)
	}
	if byteSize != int64(len(plaintext)) {
		t.Fatalf("byte size = %d, want %d", byteSize, len(plaintext))
	}
	if storedSize <= 0 {
		t.Fatalf("stored size must be positive, got %d", storedSize)
	}

	got, err := contentblob.Read(store, sha)
	testutil.FailErr(t, "read", err)
	if string(got) != string(plaintext) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestWriteIsIdempotentAndDeduplicates(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	plaintext := []byte("repeat write of the same bytes")

	sha1, byteSize1, storedSize1, err := contentblob.Write(store, plaintext)
	testutil.FailErr(t, "first write", err)
	sha2, byteSize2, storedSize2, err := contentblob.Write(store, plaintext)
	testutil.FailErr(t, "second write", err)

	if sha1 != sha2 || byteSize1 != byteSize2 || storedSize1 != storedSize2 {
		t.Fatalf("repeat write diverged: (%s,%d,%d) vs (%s,%d,%d)",
			sha1, byteSize1, storedSize1, sha2, byteSize2, storedSize2)
	}
}

func TestReadMissingDigestErrors(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	sum := sha256.Sum256([]byte("never written"))
	_, err := contentblob.Read(store, hex.EncodeToString(sum[:]))
	if !errors.Is(err, contentblob.ErrMissing) {
		t.Fatalf("a digest with no file should report ErrMissing, got %v", err)
	}
}

// A body that exists but is damaged is not a missing one: callers degrade on
// loss and refuse on damage.
func TestReadCorruptObjectIsNotReportedMissing(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	sha, _, _, err := contentblob.Write(store, []byte("intact body"))
	testutil.FailErr(t, "write", err)
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "rel path", err)
	testutil.FailErr(t, "damage object", os.WriteFile(filepath.Join(store.Root, filepath.FromSlash(rel)), []byte("not zstd"), 0o600))

	_, err = contentblob.Read(store, sha)
	if err == nil || errors.Is(err, contentblob.ErrMissing) {
		t.Fatalf("a damaged object must fail as damage, got %v", err)
	}
}

// modelOutputEnvelope mirrors session/store's content_blob_sha256 envelope
// shape without importing that package (it imports contentblob).
type modelOutputEnvelope struct {
	Content       string          `json:"content"`
	ToolCallsJSON json.RawMessage `json:"tool_calls_json,omitempty"`
	ReasoningJSON json.RawMessage `json:"reasoning_json,omitempty"`
}

func TestWriteReadModelOutputShapedEnvelope(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	envelope := modelOutputEnvelope{
		Content:       "the assistant said this",
		ToolCallsJSON: json.RawMessage(`[{"id":"call-1","name":"read","arguments":"{}"}]`),
		ReasoningJSON: json.RawMessage(`{"summary":"thinking"}`),
	}
	raw, err := json.Marshal(envelope)
	testutil.FailErr(t, "marshal envelope", err)

	sha, _, _, err := contentblob.Write(store, raw)
	testutil.FailErr(t, "write envelope", err)

	got, err := contentblob.Read(store, sha)
	testutil.FailErr(t, "read envelope", err)

	var decoded modelOutputEnvelope
	testutil.FailErr(t, "unmarshal envelope", json.Unmarshal(got, &decoded))
	if decoded.Content != envelope.Content {
		t.Fatalf("content = %q, want %q", decoded.Content, envelope.Content)
	}
	if string(decoded.ToolCallsJSON) != string(envelope.ToolCallsJSON) {
		t.Fatalf("tool_calls_json = %s, want %s", decoded.ToolCallsJSON, envelope.ToolCallsJSON)
	}
	if string(decoded.ReasoningJSON) != string(envelope.ReasoningJSON) {
		t.Fatalf("reasoning_json = %s, want %s", decoded.ReasoningJSON, envelope.ReasoningJSON)
	}
}

func TestWriteReadEvidenceShapedBody(t *testing.T) {
	store := contentblob.StoreFor(t.TempDir(), "project-1")
	body := []string{"line one", "line two", "line three"}
	raw, err := json.Marshal(body)
	testutil.FailErr(t, "marshal body", err)

	sha, _, _, err := contentblob.Write(store, raw)
	testutil.FailErr(t, "write body", err)

	got, err := contentblob.Read(store, sha)
	testutil.FailErr(t, "read body", err)

	var decoded []string
	testutil.FailErr(t, "unmarshal body", json.Unmarshal(got, &decoded))
	if len(decoded) != len(body) {
		t.Fatalf("body length = %d, want %d", len(decoded), len(body))
	}
	for i := range body {
		if decoded[i] != body[i] {
			t.Fatalf("body[%d] = %q, want %q", i, decoded[i], body[i])
		}
	}
}

func TestRelPathShardsByDigestPrefix(t *testing.T) {
	sum := sha256.Sum256([]byte("shard me"))
	sha := hex.EncodeToString(sum[:])
	rel, err := contentblob.RelPath(sha)
	testutil.FailErr(t, "rel path", err)
	want := filepath.Join(contentblob.Dir, sha[:2], sha[2:])
	if rel != want {
		t.Fatalf("rel path = %q, want %q", rel, want)
	}
}

func TestRelPathRejectsInvalidDigest(t *testing.T) {
	if _, err := contentblob.RelPath("not-a-digest"); err == nil {
		t.Fatal("expected an error for a non-hex, wrong-length digest")
	}
}

func TestStoreForResolvesUnderProjectHostDataDir(t *testing.T) {
	dataDir := t.TempDir()
	store := contentblob.StoreFor(dataDir, "project-1")
	if store.Root == "" {
		t.Fatal("expected a resolved root")
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data dir must already exist: %v", err)
	}
}
