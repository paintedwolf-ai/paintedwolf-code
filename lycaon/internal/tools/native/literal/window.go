package literal

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// window is a head/tail selection: a count of lines or bytes, or for tail
// a one-based +START.
type window struct {
	count     int64
	bytes     bool
	fromStart bool
}

func (inv *invocation) window(allowStart bool) (window, error) {
	w := window{count: 10}
	key := "-n"
	value, ok := inv.lastOf("-n", "-c")
	if ok == "-c" {
		key, w.bytes = "-c", true
	}
	if ok == "" {
		return w, nil
	}
	if allowStart && strings.HasPrefix(value, "+") {
		w.fromStart = true
		value = value[1:]
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || value == "" || value[0] == '-' || value[0] == '+' {
		return w, inv.usage(value, "nonnegative decimal count for "+key)
	}
	w.count = n
	return w, nil
}

// lastOf returns the value of whichever of the keys occurred last.
func (inv *invocation) lastOf(keys ...string) (string, string) {
	value, which := "", ""
	for _, item := range inv.argv.Items {
		if item.Option == nil || len(item.Option.Values) == 0 {
			continue
		}
		for _, key := range keys {
			if item.Option.Key == key {
				value, which = item.Option.Values[0].Value, key
			}
		}
	}
	return value, which
}

// headers follows head/tail: label each input when there are several unless
// -q, or always with -v; inputs after the first are preceded by a blank line.
type headers struct {
	inv   *invocation
	show  bool
	shown int
}

func newHeaders(inv *invocation, inputs int) *headers {
	show := inputs > 1
	for _, item := range inv.argv.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-q":
			show = false
		case "-v":
			show = true
		}
	}
	return &headers{inv: inv, show: show}
}

func (h *headers) write(label string) error {
	if !h.show {
		return nil
	}
	if label == "-" {
		label = "standard input"
	}
	prefix := ""
	if h.shown > 0 {
		prefix = "\n"
	}
	h.shown++
	return h.inv.out.text(prefix + "==> " + label + " <==\n")
}

func runHead(inv *invocation) error {
	w, err := inv.window(false)
	if err != nil {
		return err
	}
	operands := inv.operandsOrStdin()
	labels := newHeaders(inv, len(operands))
	for _, operand := range operands {
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		if err := labels.write(in.label); err != nil {
			in.close()
			return err
		}
		err = headInput(inv, in, w)
		in.close()
		if err != nil {
			return err
		}
	}
	return nil
}

var errWindowDone = errors.New("window complete")

func headInput(inv *invocation, in *input, w window) error {
	if w.bytes {
		if in.stream == nil {
			end := min(w.count, int64(len(in.data)))
			return inv.out.literal(in, in.data[:end], 0)
		}
		var offset int64
		buf := make([]byte, 32*1024)
		lr := io.LimitReader(in.stream, w.count)
		for {
			n, err := lr.Read(buf)
			if n > 0 {
				if writeErr := inv.out.literal(in, buf[:n], offset); writeErr != nil {
					return writeErr
				}
				offset += int64(n)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	if w.count == 0 {
		return nil
	}
	lines := int64(0)
	err := eachLine(in, func(f fragment) error {
		if err := inv.out.literal(in, f.data, f.offset); err != nil {
			return err
		}
		if f.ends {
			lines++
			if lines == w.count {
				return errWindowDone
			}
		}
		return nil
	})
	if errors.Is(err, errWindowDone) {
		return nil
	}
	return err
}

func runTail(inv *invocation) error {
	w, err := inv.window(true)
	if err != nil {
		return err
	}
	operands := inv.operandsOrStdin()
	labels := newHeaders(inv, len(operands))
	for _, operand := range operands {
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		if err := labels.write(in.label); err != nil {
			return err
		}
		if err := tailInput(inv, in, w); err != nil {
			return err
		}
	}
	return nil
}

// tailStart locates the window's first byte. An unterminated final line is a line.
func tailStart(data []byte, w window) int64 {
	size := int64(len(data))
	switch {
	case w.bytes && w.fromStart:
		return min(max(w.count-1, 0), size)
	case w.bytes:
		return max(size-w.count, 0)
	case w.fromStart:
		start := int64(0)
		for line := int64(1); line < w.count && start < size; line++ {
			next := bytes.IndexByte(data[start:], '\n')
			if next < 0 {
				return size
			}
			start += int64(next) + 1
		}
		return start
	}
	if w.count == 0 {
		return size
	}
	end := size
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	for found := int64(0); ; {
		i := bytes.LastIndexByte(data[:end], '\n')
		if i < 0 {
			return 0
		}
		found++
		if found == w.count {
			return int64(i) + 1
		}
		end = int64(i)
	}
}
