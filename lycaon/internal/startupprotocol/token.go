package startupprotocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// APITokenStdinEnv asks the engine to read its API bearer token as the first
// frame on its private stdin. A process's initial environment stays readable
// through the process filesystem until it disables dumps, and on macOS for its
// whole life, so the desktop never places the token there.
const APITokenStdinEnv = "LYCAON_API_TOKEN_STDIN"

// maxAPITokenBytes bounds the token frame; generated tokens are 43 bytes.
const maxAPITokenBytes = 512

// ReadAPITokenFrame reads one length-prefixed token: a big-endian uint32
// length, then the token's printable non-space ASCII bytes.
func ReadAPITokenFrame(r io.Reader) (string, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return "", fmt.Errorf("read api token length: %w", err)
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxAPITokenBytes {
		return "", fmt.Errorf("api token length must be between 1 and %d bytes", maxAPITokenBytes)
	}
	token := make([]byte, n)
	if _, err := io.ReadFull(r, token); err != nil {
		return "", fmt.Errorf("read api token: %w", err)
	}
	for _, b := range token {
		if b <= ' ' || b > '~' {
			return "", errors.New("api token holds a byte outside printable ASCII")
		}
	}
	return string(token), nil
}
