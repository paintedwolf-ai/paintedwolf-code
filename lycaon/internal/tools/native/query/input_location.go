package query

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

func (in *inputs) malformed(err error) {
	message := err.Error()
	if errors.Is(err, io.ErrUnexpectedEOF) {
		message = "Unfinished JSON term at EOF"
	}
	line, column := 1, 0
	if in.stream != nil {
		line, column = in.stream.Position()
	}
	_, _ = fmt.Fprintf(in.inv.stderr, "jq: parse error: %s at line %d, column %d\n", message, line, column)
	in.broken = true
}
func (in *inputs) position() string {
	if in.label == "" {
		return "<unknown>"
	}
	_, line := in.current()
	name := in.label
	if in.inv.pythonYq() {
		name = "<stdin>"
	}
	return fmt.Sprintf("%s:%d", name, line)
}
func (in *inputs) current() (any, int) {
	if in.label == "" {
		return nil, 0
	}
	if in.inv.pythonYq() {
		return "<stdin>", in.yamlDoc
	}
	var name any = in.label
	if in.label == "<stdin>" {
		name = nil
	}
	if in.stream != nil {
		return name, in.stream.InputLine()
	}
	if in.raw {
		return name, in.rawLines
	}
	if in.yaml {
		return name, strings.Count(in.text, "\n")
	}
	return name, in.lastLine
}
