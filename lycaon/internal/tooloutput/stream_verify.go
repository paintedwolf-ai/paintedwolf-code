package tooloutput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

// StreamReadbackMismatchError reports a retained body whose decompressed
// read-back no longer matches the size and digest recorded at staging. A
// shortened body must surface as a read error, never as a complete stream.
type StreamReadbackMismatchError struct {
	ExpectedBytes  int64
	ReceivedBytes  int64
	ExpectedSHA256 string
	ReceivedSHA256 string
}

func (e *StreamReadbackMismatchError) Error() string {
	return fmt.Sprintf("retained stream read-back mismatch: received %d bytes sha %s, staged %d bytes sha %s",
		e.ReceivedBytes, e.ReceivedSHA256, e.ExpectedBytes, e.ExpectedSHA256)
}

// verifiedStreamReadback checks a clean EOF against the staged stream's recorded
// size and digest. Storage loss below the staging boundary would otherwise
// deliver a silently shortened prefix as a complete stream. Reads that end
// before EOF are unaffected; only a claimed complete read is verified.
type verifiedStreamReadback struct {
	io.ReadCloser
	expectedBytes  int64
	expectedSHA256 string
	digest         hash.Hash
	received       int64
	failure        error
}

func verifyStreamReadback(reader io.ReadCloser, expectedBytes int64, expectedSHA256 string) io.ReadCloser {
	return &verifiedStreamReadback{ReadCloser: reader, expectedBytes: expectedBytes, expectedSHA256: expectedSHA256, digest: sha256.New()}
}

func (r *verifiedStreamReadback) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.received += int64(n)
		_, _ = r.digest.Write(p[:n])
	}
	if r.failure != nil {
		return n, r.failure
	}
	switch {
	case errors.Is(err, io.EOF):
		received := hex.EncodeToString(r.digest.Sum(nil))
		if r.received != r.expectedBytes || received != r.expectedSHA256 {
			r.failure = &StreamReadbackMismatchError{
				ExpectedBytes: r.expectedBytes, ReceivedBytes: r.received,
				ExpectedSHA256: r.expectedSHA256, ReceivedSHA256: received,
			}
			return n, r.failure
		}
	case err != nil:
		return n, err
	}
	return n, err
}
