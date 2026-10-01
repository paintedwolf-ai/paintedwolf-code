package promptattach_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
)

func TestUploadRejectsInvalidUTF8AfterDetectionWindow(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	body := append([]byte(strings.Repeat("a", 16<<10)), 0xff)
	_, err := promptattach.Upload(context.Background(), store, promptattach.Active(), nil, "notes.txt", "text/plain", bytes.NewReader(body))
	if attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("err = %v want UNSUPPORTED_ATTACHMENT", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "prompt-attachments"))
	if readErr != nil {
		t.Fatalf("read attachment dir: %v", readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("invalid text left staged blob %q", entry.Name())
		}
	}
}

func TestUploadTransportRejectionRemovesMaterializedBody(t *testing.T) {
	root := t.TempDir()
	store := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	caps := promptattach.Active()
	caps.Transport.MaxUpload = bytebound.Transport(8)
	_, err := promptattach.Upload(context.Background(), store, caps, nil, "notes.txt", "text/plain", strings.NewReader("more than eight bytes"))
	if attacherr.CodeOf(err) != attacherr.CodeTooLarge {
		t.Fatalf("err = %v want ATTACHMENT_TOO_LARGE", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "prompt-attachments"))
	if readErr != nil {
		t.Fatalf("read attachment dir: %v", readErr)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("transport rejection left staged blob %q", entry.Name())
		}
	}
}
