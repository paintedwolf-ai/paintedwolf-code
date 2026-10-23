package literal

import (
	"encoding/base64"
	"errors"
	"io"
	"strconv"
)

// base64Width is the GNU -w or BSD -b wrap column; zero disables wrapping.
func (inv *invocation) base64Width() (int64, error) {
	value, which := inv.lastOf("-w", "-b")
	if which == "" {
		return 76, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, inv.usage(value, "nonnegative wrap width for "+which)
	}
	return n, nil
}

func runBase64(inv *invocation) error {
	width, err := inv.base64Width()
	if err != nil {
		return err
	}
	in, err := inv.singleInput()
	if err != nil || in == nil {
		return err
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 64<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	var chunk [32 << 10]byte
	if inv.flag("-d") {
		_, err = io.CopyBuffer(inv.out, base64.NewDecoder(base64.StdEncoding.Strict(), base64WhitespaceReader{in.reader()}), chunk[:])
		var corrupt base64.CorruptInputError
		if errors.As(err, &corrupt) || errors.Is(err, io.ErrUnexpectedEOF) {
			inv.fail(1, "", "invalid input")
			return nil
		}
		return err
	}
	wrapped := &base64WrapWriter{out: inv.out, width: width}
	encoder := base64.NewEncoder(base64.StdEncoding, wrapped)
	if _, err = io.CopyBuffer(encoder, in.reader(), chunk[:]); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if width > 0 && wrapped.column > 0 {
		_, err = inv.out.Write([]byte{'\n'})
	}
	return err
}

type base64WhitespaceReader struct{ io.Reader }

func (r base64WhitespaceReader) Read(p []byte) (int, error) {
	for {
		n, err := r.Reader.Read(p)
		kept := 0
		for _, c := range p[:n] {
			switch c {
			case ' ', '\t', '\n', '\r', '\v', '\f':
			default:
				p[kept] = c
				kept++
			}
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}

type base64WrapWriter struct {
	out           io.Writer
	width, column int64
}

func (w *base64WrapWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if w.width > 0 {
			n = int(min(int64(n), w.width-w.column))
		}
		written, err := w.out.Write(p[:n])
		total += written
		w.column += int64(written)
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		p = p[n:]
		if w.width > 0 && w.column == w.width {
			if _, err = w.out.Write([]byte{'\n'}); err != nil {
				return total, err
			}
			w.column = 0
		}
	}
	return total, nil
}
