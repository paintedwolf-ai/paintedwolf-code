package bialy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

// CompleteMarker is written after every pinned file verified.
const CompleteMarker = ".complete"

var ensureMu sync.Mutex

// modelReady reports whether dir holds the shipped checkpoint. Files land only after
// their digest verifies, so presence plus the marker is enough.
func modelReady(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, CompleteMarker)); err != nil {
		return false
	}
	for _, f := range ShippedModel.Files {
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// Progress reports one file's download as it advances.
type Progress func(path string, done, total int64)

// EnsureModel downloads the shipped checkpoint into dir, verifying each file before it
// lands, and marks the directory complete. A complete directory is left untouched.
func EnsureModel(ctx context.Context, dir string, progress Progress) error {
	ensureMu.Lock()
	defer ensureMu.Unlock()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("decide: no model directory")
	}
	if modelReady(dir) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301 — model cache under the device configuration
		return err
	}
	model := ShippedModel
	for _, f := range model.Files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if verified(target, f) {
			continue
		}
		if err := fetchFile(ctx, model, f, target, progress); err != nil {
			return fmt.Errorf("decide: fetch %s: %w", f.Path, err)
		}
	}
	marker := filepath.Join(dir, CompleteMarker)
	if err := os.WriteFile(marker, []byte(model.Revision+"\n"), 0o644); err != nil { //nolint:gosec // G306 — revision marker is non-secret
		return err
	}
	return nil
}

// verified reports whether target already holds the pinned bytes.
func verified(target string, f ModelFile) bool {
	st, err := os.Stat(target)
	if err != nil || st.Size() != f.Size {
		return false
	}
	sum, err := fileSHA256(target)
	return err == nil && strings.EqualFold(sum, f.SHA256)
}

func fileSHA256(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchFile downloads one pinned file to a sibling temporary path, verifies
// the digest, and renames it into place; a mismatch leaves nothing behind.
func fetchFile(ctx context.Context, model Model, f ModelFile, target string, progress Progress) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301 — model cache under the device configuration
		return err
	}
	url := fmt.Sprintf("%s/%s/resolve/%s/%s", egressclass.DecideModelEndpoint, model.ID, model.Revision, f.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := httpclient.Download(egressclass.DecideModelDownload, 30*time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".partial-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	h := sha256.New()
	var done int64
	buf := make([]byte, 1<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				_ = tmp.Close()
				return err
			}
			_, _ = h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(f.Path, done, f.Size)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = tmp.Close()
			return readErr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if done != f.Size {
		return fmt.Errorf("%d bytes, pinned %d", done, f.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, f.SHA256) {
		return fmt.Errorf("sha256 mismatch: pinned %s, got %s", f.SHA256, got)
	}
	return os.Rename(tmpPath, target)
}
