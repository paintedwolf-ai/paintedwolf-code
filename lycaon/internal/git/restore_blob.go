package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Index previews use stored blobs, without checkout filters or line-ending conversion.
func readRestoreTreeFile(ctx context.Context, dir, tree, path string) (RestoreContent, error) {
	raw, err := restoreGit(ctx, dir, hermeticOpts(0), "--literal-pathspecs", "ls-tree", "-z", tree, "--", path)
	if err != nil {
		return RestoreContent{}, err
	}
	if len(raw) == 0 {
		return RestoreContent{}, nil
	}
	meta, name, ok := strings.Cut(strings.TrimSuffix(string(raw), "\x00"), "\t")
	fields := strings.Fields(meta)
	if !ok || name != path || len(fields) != 3 || fields[1] != "blob" {
		return RestoreContent{}, fmt.Errorf("restore index target is not a blob: %s", path)
	}
	mode, err := strconv.ParseUint(fields[0], 8, 32)
	if err != nil {
		return RestoreContent{}, err
	}
	fileMode := os.FileMode(mode & 0o777)
	if mode&0o170000 == 0o120000 {
		fileMode |= os.ModeSymlink
	}
	writer := &restoreBlobCapture{hash: sha256.New()}
	diagnostics, code, err := gitexec.RunTo(ctx, dir, []string{"--no-replace-objects", "cat-file", "blob", "--end-of-options", fields[2]}, hermeticOpts(0), writer)
	if err != nil {
		return RestoreContent{}, err
	}
	if code != 0 {
		return RestoreContent{}, fmt.Errorf("read restore blob: %s", diagnostics)
	}
	return RestoreContent{Exists: true, Mode: fileMode, Bytes: writer.raw, Size: writer.size, SHA256: hex.EncodeToString(writer.hash.Sum(nil))}, nil
}

type restoreBlobCapture struct {
	hash hash.Hash
	raw  []byte
	size int64
}

func (w *restoreBlobCapture) Write(raw []byte) (int, error) {
	w.size += int64(len(raw))
	_, _ = w.hash.Write(raw)
	if w.size <= sourceledger.MaxRevisionContentBytes {
		w.raw = append(w.raw, raw...)
	} else {
		w.raw = nil
	}
	return len(raw), nil
}
