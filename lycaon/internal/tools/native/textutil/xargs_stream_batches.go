package textutil

import (
	"errors"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// xargsBatcher retains only the current argv, not the complete input plan.
type xargsBatcher struct {
	inv                 *invocation
	plan                xargsPlan
	input               *xargsInput
	command, items      commandprogram.Fields
	at, lines           int
	size                int64
	pending             *xargsItem
	lineHasItems, ended bool
	batches             int64
}

func newXargsBatcher(input *xargsInput, plan xargsPlan) (*xargsBatcher, error) {
	inv := input.inv
	if err := inv.reserve(2*inv.limit("argv_bytes") + 128*inv.limit("expanded_fields")); err != nil {
		return nil, err
	}
	command := inv.operands()
	if len(command) == 0 {
		command = commandprogram.Fields{{Value: "echo", Span: inv.request.Invocation.Span}}
	}
	b := &xargsBatcher{inv: inv, plan: plan, input: input, command: command, at: len(command)}
	if plan.join != "" {
		for i, word := range command[1:] {
			if word.Value == plan.join {
				b.at = i + 1
				break
			}
		}
	}
	b.size = b.commandBytes()
	return b, nil
}

func (b *xargsBatcher) next() (commandprogram.Fields, error) {
	for !b.ended {
		item, err := b.nextItem()
		if errors.Is(err, io.EOF) {
			b.ended = true
			if len(b.items) > 0 {
				return b.flush()
			}
			if b.batches == 0 && !b.plan.replacing && !b.inv.flag("-r") {
				return b.accept(b.command)
			}
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		if item.present {
			if strings.IndexByte(item.value, 0) >= 0 {
				return nil, b.inv.usage(b.inv.commandWord(), "items without NUL bytes")
			}
			if b.plan.replacing {
				argv, err := b.plan.replaced(b.inv, b.command, [][]string{{item.value}})
				if err != nil {
					return nil, err
				}
				return b.accept(argv[0])
			}
			next := int64(len(item.value))
			tooLarge := b.size+next > b.plan.maxChars || int64(len(b.command)+len(b.items)+1) > b.inv.limit("expanded_fields")
			if tooLarge && len(b.items) > 0 {
				if b.plan.count > 0 && b.plan.exact {
					return nil, tools.CommandLimitExceeded("argv_bytes", b.plan.maxChars, b.size+next)
				}
				b.pending = &item
				return b.flush()
			}
			if tooLarge {
				return nil, tools.CommandLimitExceeded("argv_bytes", b.plan.maxChars, b.size+next)
			}
			b.items = append(b.items, commandprogram.Field{Value: item.value, Span: b.inv.request.Invocation.Span})
			b.size += next
			b.lineHasItems = true
		}
		if item.lineEnd && b.lineHasItems {
			b.lines++
			b.lineHasItems = false
		}
		if (b.plan.count > 0 && len(b.items) >= b.plan.count) || (b.plan.lines > 0 && b.lines >= b.plan.lines) {
			return b.flush()
		}
	}
	return nil, io.EOF
}

func (b *xargsBatcher) nextItem() (xargsItem, error) {
	if b.pending != nil {
		item := *b.pending
		b.pending = nil
		return item, nil
	}
	return b.input.next()
}

func (b *xargsBatcher) flush() (commandprogram.Fields, error) {
	argv := append(append(commandprogram.Fields{}, b.command[:b.at]...), b.items...)
	if b.at < len(b.command) {
		argv = append(argv, b.command[b.at+1:]...)
	}
	b.items, b.size, b.lines = nil, b.commandBytes(), 0
	return b.accept(argv)
}

func (b *xargsBatcher) accept(argv commandprogram.Fields) (commandprogram.Fields, error) {
	b.batches++
	if maximum := b.inv.limit("child_executions"); b.batches > maximum {
		return nil, tools.CommandLimitExceeded("child_executions", maximum, b.batches)
	}
	return argv, nil
}

func (b *xargsBatcher) commandBytes() int64 {
	var size int64
	for i, word := range b.command {
		if i != b.at {
			size += int64(len(word.Value))
		}
	}
	return size
}
