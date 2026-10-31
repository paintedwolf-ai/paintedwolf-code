package textutil

import (
	"bufio"
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"strings"
)

// xargsInput keeps one bounded item and the quoting state between reads.
type xargsInput struct {
	inv              *invocation
	ctx              context.Context
	reader           *bufio.Reader
	delimiter        byte
	exact, replacing bool
	eof              *string
	read             int64
	ended, blankEnd  bool
}

type xargsItem struct {
	value            string
	present, lineEnd bool
}

func (x xargsRun) input(plan xargsPlan) (*xargsInput, error) {
	delimiter, exact, err := x.delimiter()
	if err != nil {
		return nil, err
	}
	if err := x.inv.reserve(64<<10 + 2*x.inv.limit("argument_bytes")); err != nil {
		return nil, err
	}
	return &xargsInput{inv: x.inv, ctx: x.inv.ctx, reader: bufio.NewReaderSize(x.inv.streams.Input, 64<<10), delimiter: delimiter, exact: exact, replacing: plan.replacing, eof: plan.eof}, nil
}

func (in *xargsInput) readByte() (byte, error) {
	if err := in.ctx.Err(); err != nil {
		return 0, err
	}
	b, err := in.reader.ReadByte()
	if err == nil {
		in.read++
		if maximum := in.inv.limit("stream_throughput_bytes"); in.read > maximum {
			return 0, tools.CommandLimitExceeded("stream_throughput_bytes", maximum, in.read)
		}
	}
	return b, err
}

func (in *xargsInput) next() (xargsItem, error) {
	if in.ended {
		return xargsItem{}, io.EOF
	}
	var item strings.Builder
	present, quote := false, byte(0)
	for {
		b, err := in.readByte()
		if errors.Is(err, io.EOF) {
			in.ended = true
			if quote != 0 {
				return xargsItem{}, in.inv.usage(in.inv.commandWord(), "input with matched quotes")
			}
			if !present {
				return xargsItem{}, io.EOF
			}
			return in.item(item.String(), true, true), nil
		}
		if err != nil {
			return xargsItem{}, err
		}
		if in.exact || in.replacing {
			separator := in.delimiter
			if !in.exact {
				separator = '\n'
				if !present && (b == ' ' || b == '\t') {
					continue
				}
			}
			if b == separator {
				if !in.exact && !present {
					continue
				}
				return in.item(item.String(), true, true), nil
			}
		} else {
			switch {
			case quote != 0 && b == quote:
				quote = 0
				continue
			case quote != 0:
				if b == '\n' {
					return xargsItem{}, in.inv.usage(in.inv.commandWord(), "input with matched quotes on one line")
				}
			case b == '\'' || b == '"':
				quote, present, in.blankEnd = b, true, false
				continue
			case b == '\\':
				next, err := in.readByte()
				if err != nil && !errors.Is(err, io.EOF) {
					return xargsItem{}, err
				}
				if err == nil {
					b = next
				}
			case b == '\n':
				return in.item(item.String(), present, !in.blankEnd), nil
			case b == ' ' || b == '\t':
				in.blankEnd = true
				if present {
					return in.item(item.String(), true, false), nil
				}
				continue
			}
		}
		if maximum := in.inv.limit("argument_bytes"); int64(item.Len()+1) > maximum {
			return xargsItem{}, tools.CommandLimitExceeded("argument_bytes", maximum, int64(item.Len()+1))
		}
		item.WriteByte(b)
		present, in.blankEnd = true, false
	}
}

func (in *xargsInput) item(value string, present, lineEnd bool) xargsItem {
	if !in.exact && !in.replacing && present && in.eof != nil && value == *in.eof {
		in.ended = true
		return xargsItem{lineEnd: true}
	}
	return xargsItem{value: value, present: present, lineEnd: lineEnd}
}
