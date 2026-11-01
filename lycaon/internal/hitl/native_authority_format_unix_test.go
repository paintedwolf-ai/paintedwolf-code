//go:build !windows

package hitl_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPersistedDirectoryAuthorityRemainsReadable(t *testing.T) {
	raw, err := os.ReadFile("testdata/native-directory-authority-v1.json")
	testutil.FailErr(t, "read pending directory fixture", err)
	sum := sha256.Sum256(raw)
	memory := productionAuthorityMemory(t)
	projection, release, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, hitl.NativeAuthorityFormat, hex.EncodeToString(sum[:]), string(raw))
	testutil.FailErr(t, "restore persisted directory authority", err)
	options, releaseOptions, err := projection.Options(t.Context(), memory)
	testutil.FailErr(t, "inspect persisted directory choices", err)
	if len(options) != 22 {
		t.Fatalf("restored %d directory choices", len(options))
	}
	encoded, releaseEncoded, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, options)
	testutil.FailErr(t, "encode persisted directory choices", err)
	if encoded.Canonical() != string(raw) {
		t.Fatal("persisted directory authority changed representation")
	}
	releaseEncoded()
	releaseOptions()
	release()
	if memory.InUse().State != 0 {
		t.Fatal("persisted directory restore retained scratch")
	}
}
