package literal

import (
	"bufio"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// manifestRecords keeps the original manifest stream open while each named
// target acquires and releases its own capture. It never recaptures the manifest.
func (inv *invocation) manifestRecords(operand commandprogram.Field, consume func(int, string) error) (finalErr error) {
	in, err := inv.openField(operand, false)
	if err != nil || in == nil {
		return err
	}
	manifest := inv.currentStream
	inv.currentStream = nil
	if manifest != nil {
		defer func() {
			if closeErr := manifest.Close(); closeErr != nil {
				if finalErr == nil {
					finalErr = closeErr
				} else {
					finalErr = errors.Join(finalErr, closeErr)
				}
			}
		}()
	}
	defer inv.closeCurrentStream()
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	reader := bufio.NewReaderSize(in.reader(), 64<<10)
	buffer, err := inv.owners.Revisions.NewBufferedBytes(inv.ctx, inv.owners.limit("record_bytes"), "record_bytes")
	if err != nil {
		return err
	}
	defer buffer.Close()
	for number := 1; ; number++ {
		if err := buffer.Reset(); err != nil {
			return err
		}
		any := false
		var failure error
		for {
			chunk, err := reader.ReadSlice('\n')
			any = any || len(chunk) > 0
			if err == nil {
				chunk = chunk[:len(chunk)-1]
			}
			if _, failure = buffer.Write(chunk); failure != nil {
				return failure
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			failure = err
			break
		}
		if failure != nil && !errors.Is(failure, io.EOF) {
			return failure
		}
		if !any {
			return nil
		}
		release := func() {}
		if memory := inv.owners.Revisions.Memory; memory != nil {
			release, err = memory.Reserve(inv.ctx, int64(len(buffer.Bytes)))
			if err != nil {
				return err
			}
		}
		err = consume(number, string(buffer.Bytes))
		release()
		inv.closeCurrentStream()
		if err != nil {
			return err
		}
		if errors.Is(failure, io.EOF) {
			return nil
		}
	}
}
