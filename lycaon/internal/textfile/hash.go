package textfile

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256 is the stable identity of the exact on-disk bytes.
func SHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// StringSHA256 hashes text with fixed scratch instead of converting the
// complete backing string to a second byte slice.
func StringSHA256(content string) string {
	hash := sha256.New()
	var chunk [32 << 10]byte
	for len(content) > 0 {
		n := copy(chunk[:], content)
		_, _ = hash.Write(chunk[:n])
		content = content[n:]
	}
	return hex.EncodeToString(hash.Sum(nil))
}
