package contentblob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesDamagedBlobWithVerifiedBytesOnly(t *testing.T) {
	store := StoreFor(t.TempDir(), "proj-1")
	payload := []byte(`{"body":"package demo\n\nfunc First() int { return 1 }\n"}`)

	sha, _, _, err := Write(context.Background(), store, payload)
	if err != nil {
		t.Fatalf("initial Write: %v", err)
	}
	rel, err := RelPath(sha)
	if err != nil {
		t.Fatalf("RelPath: %v", err)
	}
	abs := filepath.Join(store.Root, filepath.FromSlash(rel))

	// Verified blob must not be replaced on repeat Write.
	origInfo, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat initial blob: %v", err)
	}
	if _, _, _, err := Write(context.Background(), store, payload); err != nil {
		t.Fatalf("repeat Write on verified blob: %v", err)
	}
	afterInfo, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat repeat blob: %v", err)
	}
	if !os.SameFile(origInfo, afterInfo) {
		t.Fatalf("verified blob was unnecessarily replaced")
	}

	// Corrupt the stored blob bytes so verification fails.
	if err := os.WriteFile(abs, []byte("corrupt-not-valid-zstd"), 0o600); err != nil {
		t.Fatalf("corrupt blob: %v", err)
	}
	if _, err := Read(store, sha); err == nil {
		t.Fatalf("expected Read to fail on corrupted blob")
	}

	// Re-writing the verified plaintext atomically replaces the damaged blob.
	sha2, _, _, err := Write(context.Background(), store, payload)
	if err != nil {
		t.Fatalf("recovery Write: %v", err)
	}
	if sha2 != sha {
		t.Fatalf("digest mismatch: got %s, want %s", sha2, sha)
	}
	got, err := Read(store, sha)
	if err != nil {
		t.Fatalf("Read after recovery Write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("recovered payload mismatch: got %q, want %q", got, payload)
	}
}

func TestWriteWithChangedContentLeavesDamagedOldBlobUntouched(t *testing.T) {
	store := StoreFor(t.TempDir(), "proj-2")
	oldPayload := []byte(`{"body":"package demo\n\nfunc First() int { return 1 }\n"}`)
	newPayload := []byte(`{"body":"package demo\n\nfunc First() int { return 2 }\n"}`)

	oldSHA, _, _, err := Write(context.Background(), store, oldPayload)
	if err != nil {
		t.Fatalf("Write old: %v", err)
	}
	oldRel, err := RelPath(oldSHA)
	if err != nil {
		t.Fatalf("RelPath old: %v", err)
	}
	oldAbs := filepath.Join(store.Root, filepath.FromSlash(oldRel))
	damaged := []byte("corrupt-old-blob")
	if err := os.WriteFile(oldAbs, damaged, 0o600); err != nil {
		t.Fatalf("corrupt old blob: %v", err)
	}

	// Writing changed file content produces a different digest and must not touch the old damaged blob.
	newSHA, _, _, err := Write(context.Background(), store, newPayload)
	if err != nil {
		t.Fatalf("Write new: %v", err)
	}
	if newSHA == oldSHA {
		t.Fatalf("expected distinct digests for changed content")
	}
	if gotNew, err := Read(store, newSHA); err != nil || !bytes.Equal(gotNew, newPayload) {
		t.Fatalf("Read new = (%q, %v), want %q", gotNew, err, newPayload)
	}
	if _, err := Read(store, oldSHA); err == nil {
		t.Fatalf("expected old damaged blob to remain unreadable")
	}
	if rawOld, err := os.ReadFile(oldAbs); err != nil || !bytes.Equal(rawOld, damaged) {
		t.Fatalf("old damaged blob was modified: got %q, err=%v", rawOld, err)
	}
}
