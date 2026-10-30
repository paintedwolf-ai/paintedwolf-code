package gitcmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type blobFeedbackBackend struct {
	Backend
	content          []byte
	available        bool
	readErr, sizeErr error
	size             int64
	sizeCalls        int
}

func (b *blobFeedbackBackend) BlobContent(context.Context, string, string, int) ([]byte, bool, error) {
	return b.content, b.available, b.readErr
}
func (b *blobFeedbackBackend) BlobSize(context.Context, string, string) (int64, error) {
	b.sizeCalls++
	return b.size, b.sizeErr
}

func TestGitEntryContentReportsOnlyMeasuredOverflow(t *testing.T) {
	refused := errors.New("object metadata refused")
	for _, tc := range []struct {
		name    string
		backend blobFeedbackBackend
		limit   bool
		wantErr error
		success bool
	}{
		{name: "oversized", backend: blobFeedbackBackend{size: 64}, limit: true},
		{name: "missing", backend: blobFeedbackBackend{size: 16}},
		{name: "metadata refusal", backend: blobFeedbackBackend{sizeErr: refused}, wantErr: refused},
		{name: "content refusal", backend: blobFeedbackBackend{readErr: refused}, wantErr: refused},
		{name: "ordinary content", backend: blobFeedbackBackend{content: []byte("abc"), available: true}, success: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := tc.backend
			owners, err := NewOwners(&backend)
			testutil.FailErr(t, "compose measured blob owner", err)
			owners.limitOverride = map[string]int64{"stream_bytes": 32}
			data, err := entryContent(t.Context(), Prepared{owners: owners}, "repo", trackedEntry{oid: "captured-object", path: "file"})
			reject := toolrejection.AsToolReject(err)
			if tc.success {
				if err != nil || string(data) != "abc" || backend.sizeCalls != 0 {
					t.Fatalf("successful read repeated metadata or changed bytes: %q err=%v sizeCalls=%d", data, err, backend.sizeCalls)
				}
				return
			}
			if err == nil {
				t.Fatal("unavailable blob accepted")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("lost owner refusal: %v", err)
			}
			if tc.limit {
				if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["observed"] != int64(64) || reject.Data["resource_unit"] != "bytes" || reject.Data["maximum"] != int64(32) {
					t.Fatalf("unmeasured limit: %+v", reject)
				}
			} else if reject != nil {
				t.Fatalf("non-overflow became a resource refusal: %+v", reject)
			}
		})
	}
}

func TestGitGrepLimitFeedbackCarriesMeasuredBytes(t *testing.T) {
	h := newHarness(t)
	h.write("oversized.txt", strings.Repeat("x", 1024))
	h.git("add", "oversized.txt")
	h.owners.limitOverride = map[string]int64{"stream_bytes": 128}
	for _, tc := range []struct {
		name     string
		args     []string
		observed int64
	}{
		{"index", []string{"git", "grep", "--cached", "x", "--", "oversized.txt"}, 1024},
		{"worktree", []string{"git", "grep", "x", "--", "oversized.txt"}, 129},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := h.exec(tc.args...)
			reject := toolrejection.AsToolReject(result.err)
			if reject == nil || reject.Code != "COMMAND_LIMIT_EXCEEDED" || reject.Data["resource_unit"] != "bytes" || reject.Data["observed"] != tc.observed || reject.Data["maximum"] != int64(128) || result.stdout != "" {
				t.Fatalf("limit=%+v result=%+v", reject, result)
			}
		})
	}
}
