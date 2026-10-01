package bundled

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAttestedDescriptorPreservesLocalAndHTTPSBytes(t *testing.T) {
	t.Parallel()
	raw := []byte("not parsed before authentication\n")
	local := filepath.Join(t.TempDir(), "release.json")
	testutil.FailErr(t, "write local descriptor", os.WriteFile(local, raw, 0o600))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	for _, location := range []string{local, server.URL} {
		got, err := readAttestedDescriptor(t.Context(), AttestedReleaseSelection{Descriptor: location, Fetch: ReleaseFetchOptions{Client: server.Client()}})
		testutil.FailErr(t, "read descriptor bytes", err)
		if !bytes.Equal(got, raw) {
			t.Fatal("descriptor changed before authentication")
		}
	}
}

func TestAttestedDescriptorRejectsInvalidSizeAndCancellation(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, maxReleaseManifestBytes + 1} {
		local := filepath.Join(t.TempDir(), "release.json")
		testutil.FailErr(t, "write invalid descriptor", os.WriteFile(local, bytes.Repeat([]byte(" "), size), 0o600))
		if _, err := readAttestedDescriptor(t.Context(), AttestedReleaseSelection{Descriptor: local}); err == nil {
			t.Fatalf("accepted descriptor size %d", size)
		}
	}
	local := filepath.Join(t.TempDir(), "release.json")
	testutil.FailErr(t, "write canceled descriptor", os.WriteFile(local, []byte("{}"), 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readAttestedDescriptor(ctx, AttestedReleaseSelection{Descriptor: local}); !errors.Is(err, context.Canceled) {
		t.Fatalf("read canceled descriptor: got %v", err)
	}
}
