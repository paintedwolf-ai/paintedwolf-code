package gittest

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1" // #nosec G505 -- Git object format.
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// WriteLooseObject stores a raw Git fixture and returns its object ID.
func WriteLooseObject(objectsDir, objType string, payload []byte) (string, error) {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(payload))), payload...)
	digest := sha1.Sum(raw) // #nosec G401 -- Git object format.
	oid := hex.EncodeToString(digest[:])
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, wErr := zw.Write(raw)
	if err := errors.Join(wErr, zw.Close()); err != nil {
		return "", err
	}
	dir := filepath.Join(objectsDir, oid[:2])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: oid[2:]},
		Source:   bytes.NewReader(compressed.Bytes()),
		Mode:     0o444,
		DirMode:  0o750,
	})
	return oid, err
}
