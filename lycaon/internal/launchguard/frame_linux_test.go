//go:build linux

package launchguard

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEntryFrameRequiresRetainedAuthority(t *testing.T) {
	for _, generation := range []*Generation{nil, {}} {
		file, err := CreateEntryFrame(generation, Identity{UID: 1000, GID: 1000}, nil, nil)
		if err == nil {
			_ = file.Close()
			t.Fatal("created entry authority without retained installed generation")
		}
	}
}

func TestSealAndReadEntrySpec(t *testing.T) {
	withTestTrustAnchor(t, t.TempDir())
	spec := entryFixture()
	file, err := sealEntrySpec(spec)
	testutil.FailErr(t, "sealEntrySpec", err)
	defer func() { _ = file.Close() }()

	decoded, err := readEntry(file)
	testutil.FailErr(t, "readEntry", err)
	if decoded.UID != spec.UID || decoded.Image.Path != spec.Image.Path {
		t.Fatalf("decoded %+v != %+v", decoded, spec)
	}

	unsealed := filepathJoinTempFile(t, "{}")
	if _, err := readEntry(unsealed); err == nil {
		t.Fatal("accepted unsealed entry file")
	}

	oversized := entryFixture()
	oversized.Arguments = []string{strings.Repeat("a", maxEntryBytes+1)}
	if f, err := sealEntrySpec(oversized); err == nil {
		_ = f.Close()
		t.Fatal("accepted oversized entry spec")
	}
}

func filepathJoinTempFile(t *testing.T, content string) *os.File {
	t.Helper()
	path := t.TempDir() + "/unsealed"
	testutil.FailErr(t, "write temp", os.WriteFile(path, []byte(content), 0o400))
	testutil.FailErr(t, "chmod temp", os.Chmod(path, 0o400))
	f, err := os.Open(path)
	testutil.FailErr(t, "open temp", err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
