package query

import (
	"bufio"
	"errors"
	"io"
)

// rawLine bounds the current record before copying it into a jq string. The
// previous string's lease ends only when the query asks for its next input.
func (in *inputs) rawLine() (string, error) {
	if in.rawWork == nil {
		in.rawWork = &queryWorking{inv: in.inv}
	}
	if err := in.rawWork.resize(0); err != nil {
		return "", err
	}
	buffer, err := in.inv.owners.Revisions.NewBufferedBytes(in.inv.ctx, in.inv.owners.limit("record_bytes"), "record_bytes")
	if err != nil {
		return "", err
	}
	defer buffer.Close()
	read := false
	for {
		chunk, failure := in.rawReader.ReadSlice('\n')
		in.rawOffset += int64(len(chunk))
		read = read || len(chunk) > 0
		if failure == nil {
			chunk = chunk[:len(chunk)-1]
		}
		if _, err = buffer.Write(chunk); err != nil {
			return "", err
		}
		if in.stop != nil {
			return "", in.stop
		}
		if errors.Is(failure, bufio.ErrBufferFull) {
			continue
		}
		if failure != nil && !errors.Is(failure, io.EOF) {
			return "", failure
		}
		if !read && errors.Is(failure, io.EOF) {
			return "", io.EOF
		}
		if err := in.rawWork.resize(int64(len(buffer.Bytes))); err != nil {
			return "", err
		}
		return string(buffer.Bytes), nil
	}
}

// slurpRaw is deliberately whole-input: every reached operand contributes to
// one string and one aggregate whole-file bound, using the capture owner.
func (in *inputs) slurpRaw() (string, bool) {
	buffer, err := in.inv.owners.Revisions.NewBufferedBytes(in.inv.ctx, in.inv.owners.limit("whole_file_bytes"), "whole_file_bytes")
	if err != nil {
		in.stop = err
		return "", false
	}
	defer buffer.Close()
	work := &queryWorking{inv: in.inv}
	if err = work.resize(32 << 10); err != nil {
		in.stop = err
		return "", false
	}
	in.inv.releases = append(in.inv.releases, work.Close)
	var chunk [32 << 10]byte
	for len(in.operands) > 0 && in.stop == nil {
		operand := in.operands[0]
		in.operands = in.operands[1:]
		in.close()
		in.label = operand.Value
		in.openStream(operand)
		if in.stop != nil {
			return "", false
		}
		if in.rawReader == nil {
			continue
		}
		_, err = io.CopyBuffer(buffer, in.rawReader, chunk[:])
		in.rawReader = nil
		in.close()
		if err != nil {
			in.stop = err
			return "", false
		}
	}
	if in.stop != nil {
		return "", false
	}
	if err = work.resize(int64(len(buffer.Bytes))); err != nil {
		in.stop = err
		return "", false
	}
	return string(buffer.Bytes), true
}
