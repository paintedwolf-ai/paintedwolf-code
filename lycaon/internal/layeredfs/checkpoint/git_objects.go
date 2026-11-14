package checkpoint

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteLooseGitObject writes a zlib-compressed loose Git object into objectsDir and returns its
// SHA-1 hex OID.
func WriteLooseGitObject(objectsDir, objType string, payload []byte) (string, error) {
	raw := append([]byte(fmt.Sprintf("%s %d\x00", objType, len(payload))), payload...)
	oid := hexObjSHA1(raw)
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, wErr := zw.Write(raw)
	if err := errors.Join(wErr, zw.Close()); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(objectsDir, oid[:2]), 0o750); err != nil {
		return "", err
	}
	return oid, writeAtomicJSONOrText(filepath.Join(objectsDir, oid[:2]), oid[2:], compressed.Bytes(), 0o444)
}
