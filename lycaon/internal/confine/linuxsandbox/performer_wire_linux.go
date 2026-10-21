//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"errors"
)

// Every call crosses to the performer in a fixed binary frame: decoding JSON
// there was a measurable share of each create's cost.
//
//	op u8 | absolute u8 | dirs u8 | reserved u8 | mode u32 | umask u32 |
//	flags u64 | dev u64 | resolve u64 | notification u64 | caller u64 |
//	then path, new path and content, each a u32 length and its bytes.
const pathCallHeader = 4 + 4 + 4 + 8*5

var callOps = []string{"", callOpen, callMkdir, callMknod, callSymlink, callLink, callRename, callListener}

func callOpCode(op string) (byte, bool) {
	for i, name := range callOps {
		if i > 0 && name == op {
			return byte(i), true
		}
	}
	return 0, false
}

// encodePathCall frames one call.
func encodePathCall(c pathCall) ([]byte, error) {
	op, ok := callOpCode(c.Op)
	if !ok {
		return nil, errors.New("unknown performer call")
	}
	if c.Dirs < 0 || c.Dirs > 2 || c.Caller < 0 {
		return nil, errors.New("invalid performer call")
	}
	buf := make([]byte, pathCallHeader, pathCallHeader+12+len(c.Path)+len(c.NewPath)+len(c.Content))
	buf[0], buf[1], buf[2] = op, c.Absolute, byte(c.Dirs)
	le := binary.LittleEndian
	le.PutUint32(buf[4:], c.Mode)
	le.PutUint32(buf[8:], c.Umask)
	le.PutUint64(buf[12:], c.Flags)
	le.PutUint64(buf[20:], c.Dev)
	le.PutUint64(buf[28:], c.Resolve)
	le.PutUint64(buf[36:], c.Notification)
	le.PutUint64(buf[44:], uint64(c.Caller))
	for _, s := range []string{c.Path, c.NewPath, c.Content} {
		buf = le.AppendUint32(buf, uint32(len(s)))
		buf = append(buf, s...)
	}
	return buf, nil
}

// decodePathCall reads one frame, refusing anything malformed with an empty
// call, so no part of a bad frame (such as its notification) is acted on.
func decodePathCall(buf []byte) (pathCall, error) {
	c, err := decodeFrame(buf)
	if err != nil {
		return pathCall{}, err
	}
	return c, nil
}

func decodeFrame(buf []byte) (pathCall, error) {
	var c pathCall
	if len(buf) < pathCallHeader || buf[3] != 0 || int(buf[0]) == 0 || int(buf[0]) >= len(callOps) || buf[2] > 2 {
		return c, errors.New("malformed performer call")
	}
	le := binary.LittleEndian
	c.Op, c.Absolute, c.Dirs = callOps[buf[0]], buf[1], int(buf[2])
	c.Mode = le.Uint32(buf[4:])
	c.Umask = le.Uint32(buf[8:])
	c.Flags = le.Uint64(buf[12:])
	c.Dev = le.Uint64(buf[20:])
	c.Resolve = le.Uint64(buf[28:])
	c.Notification = le.Uint64(buf[36:])
	caller := le.Uint64(buf[44:])
	if caller > 1<<31 {
		return c, errors.New("malformed performer call")
	}
	c.Caller = int(caller)
	rest := buf[pathCallHeader:]
	var strs [3]string
	for i := range strs {
		if len(rest) < 4 {
			return c, errors.New("truncated performer call")
		}
		n := le.Uint32(rest)
		rest = rest[4:]
		if uint64(n) > uint64(len(rest)) {
			return c, errors.New("truncated performer call")
		}
		strs[i], rest = string(rest[:n]), rest[n:]
	}
	if len(rest) != 0 {
		return c, errors.New("performer call has trailing data")
	}
	c.Path, c.NewPath, c.Content = strs[0], strs[1], strs[2]
	return c, nil
}
