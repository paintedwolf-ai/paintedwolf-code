package promptattach_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/pkg/api"
)

// An attachment names a staged blob; a host path in its place is refused and
// that file's bytes never reach the turn.
func TestIngestAttachmentsRefusesAHostPathHandle(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	caps := promptattach.Active()
	const staged, host = "staged-sentinel-7f3a", "host-sentinel-91c2"
	receipt, err := promptattach.Upload(context.Background(), store, caps, nil, "notes.txt", "text/plain", bytes.NewReader([]byte(staged)))
	if err != nil {
		t.Fatalf("stage blob: %v", err)
	}
	hostFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(hostFile, []byte(host), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
		[]api.PromptAttachmentPart{{BlobID: receipt.BlobID}})
	if err != nil || !strings.Contains(strings.Join(res.Fences(), "\n"), staged) {
		t.Fatalf("staged blob: err=%v fences=%q", err, res.Fences())
	}

	rel, err := filepath.Rel(filepath.Join(root, "prompt-attachments"), hostFile)
	if err != nil {
		t.Fatalf("relative host path: %v", err)
	}
	for _, handle := range []string{hostFile, rel, "file://" + hostFile} {
		res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
			[]api.PromptAttachmentPart{{BlobID: handle}})
		if attacherr.CodeOf(err) != attacherr.CodeNotFound {
			t.Errorf("handle %q: err = %v, want %s", handle, err, attacherr.CodeNotFound)
		}
		if len(res.Parts) != 0 || strings.Contains(strings.Join(res.Fences(), "\n"), host) {
			t.Errorf("handle %q framed host bytes: %+v", handle, res.Parts)
		}
	}
}
