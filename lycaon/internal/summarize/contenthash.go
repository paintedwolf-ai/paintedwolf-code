package summarize

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	KindFile   = "file"
	KindInline = "inline"
)

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func HashString(s string) string {
	return hashBytes([]byte(s))
}
