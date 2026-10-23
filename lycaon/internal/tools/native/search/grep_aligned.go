package search

import (
	"bytes"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"io"

	"github.com/lycaon/lycaon/internal/scratch"
)

// -T needs the final input size to align numeric prefixes. A non-seekable
// input uses one anonymous, quota-admitted spool; file inputs already have the
// captured descriptor's exact byte count and never enter this path.
func (r *grepRun) searchAlignedStdin() (finalErr error) {
	inv := r.inv
	if inv.owners.Admission == nil {
		return errors.New("aligned stdin requires program spill admission")
	}
	maximum := inv.owners.limit("spill_bytes")
	release, err := inv.owners.Admission.ReserveSpill(inv.ctx, maximum)
	if err != nil {
		return err
	}
	defer release()
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	spool, err := scratch.NewByteSpool()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := spool.Close(); closeErr != nil {
			if finalErr == nil {
				finalErr = closeErr
			} else {
				finalErr = errors.Join(finalErr, closeErr)
			}
		}
	}()
	var chunk [64 << 10]byte
	var size int64
	firstNull := -1
	for {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		n, err := inv.input.Read(chunk[:])
		if n > 0 {
			if int64(n) > maximum-size {
				return tools.CommandLimitExceeded("spill_bytes", maximum, size+int64(n))
			}
			if firstNull < 0 {
				if at := bytes.IndexByte(chunk[:n], 0); at >= 0 {
					firstNull = int(size) + at
				}
			}
			written, failure := spool.Write(chunk[:n])
			if failure != nil {
				return failure
			}
			if written != n {
				return io.ErrShortWrite
			}
			size += int64(n)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	reader, err := spool.Reader()
	if err != nil {
		return err
	}
	return r.searchStream("(standard input)", reader, 0, int(size), firstNull, nil, r.forcedNames(false))
}
