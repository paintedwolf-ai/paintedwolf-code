package projectsource

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/sourceledger"
)

// Entries use lexical depth-first order in both live trees and recovery manifests.
type sourceTreeDigest struct{ hash hash.Hash }

func newSourceTreeDigest() *sourceTreeDigest { return &sourceTreeDigest{hash: sha256.New()} }

func (d *sourceTreeDigest) append(entry sourceledger.RecoveryEntry) error {
	return writeSourceTreeEntry(d.hash, entry)
}

func (d *sourceTreeDigest) sum() string { return hex.EncodeToString(d.hash.Sum(nil)) }

func writeSourceTreeEntry(out io.Writer, entry sourceledger.RecoveryEntry) error {
	_, _ = fmt.Fprintf(out, "%s\x00%d\x00", entry.Path, entry.Mode)
	mode := os.FileMode(entry.Mode)
	switch {
	case mode&os.ModeSymlink != 0:
		digest := sha256.Sum256([]byte(entry.Link))
		_, _ = out.Write(digest[:])
	case mode.IsRegular():
		digest, err := hex.DecodeString(entry.SHA)
		if err != nil || len(digest) != sha256.Size {
			return ErrSourceRecoveryFailed
		}
		_, _ = out.Write(digest)
	case mode.IsDir():
	default:
		return ErrSourceRecoveryFailed
	}
	return nil
}

// History retains only small file bodies, independently of complete recovery.
type sourceHistoryBuffer struct {
	body []byte
	size int64
}

func (b *sourceHistoryBuffer) Write(data []byte) (int, error) {
	b.size += int64(len(data))
	if b.size <= sourceledger.MaxRevisionContentBytes {
		b.body = append(b.body, data...)
	} else {
		b.body = nil
	}
	return len(data), nil
}
