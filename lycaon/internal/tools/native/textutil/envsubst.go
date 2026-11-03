package textutil

import (
	"bufio"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// envsubstReference is one $NAME or ${NAME} found in text: its name and the
// exact bytes that spelled it.
type envsubstReference struct {
	name, spelling string
}

// runEnvsubst copies standard input to standard output with $NAME and
// ${NAME} replaced from the statement's exported environment. Values are
// the frame's own text, so a managed secret reference stays a reference.
func runEnvsubst(inv *invocation) error {
	operands := inv.operands()
	if len(operands) > 1 {
		return inv.usage(operands[1], "at most one SHELL-FORMAT")
	}
	if inv.flag("-v") {
		if len(operands) == 0 {
			return inv.usage(inv.commandWord(), "SHELL-FORMAT for -v")
		}
		var names strings.Builder
		for _, ref := range envsubstReferences(operands[0].Value) {
			names.WriteString(ref.name + "\n")
		}
		_, err := io.WriteString(inv.out, names.String())
		return err
	}
	var only map[string]bool
	if len(operands) == 1 {
		only = map[string]bool{}
		for _, ref := range envsubstReferences(operands[0].Value) {
			only[ref.name] = true
		}
	}
	values := map[string]string{}
	for _, entry := range inv.request.Environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	reader := bufio.NewReaderSize(inv.streams.Input, 32<<10)
	writer := bufio.NewWriterSize(inv.out, 32<<10)
	maximum := inv.limit("record_bytes")
	for {
		text, err := reader.ReadSlice('$')
		found := err == nil
		if found {
			text = text[:len(text)-1]
		}
		if _, werr := writer.Write(text); werr != nil {
			return werr
		}
		if errors.Is(err, io.EOF) {
			return writer.Flush()
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
		if !found {
			continue
		}
		ref, err := readReference(reader, maximum)
		if err != nil {
			return err
		}
		replacement := "$" + ref.spelling
		if ref.name != "" && (only == nil || only[ref.name]) {
			replacement = values[ref.name]
		}
		if _, err := writer.WriteString(replacement); err != nil {
			return err
		}
		if err := inv.ctx.Err(); err != nil {
			return err
		}
	}
}

// readReference reads what follows a $: a name or a braced name. The
// spelling excludes the $; a reference that is not substituted has no name
// and is copied as spelled.
func readReference(reader *bufio.Reader, maximum int64) (envsubstReference, error) {
	var spelling strings.Builder
	braced := false
	if next, err := reader.Peek(1); err == nil && next[0] == '{' {
		braced = true
		_, _ = reader.ReadByte()
		spelling.WriteByte('{')
	}
	var name strings.Builder
	for {
		c, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return envsubstReference{}, err
		}
		if !nameByte(c, name.Len() == 0) {
			_ = reader.UnreadByte()
			break
		}
		if int64(name.Len()) >= maximum {
			return envsubstReference{}, tools.CommandLimitExceeded("record_bytes", maximum, int64(name.Len())+1)
		}
		name.WriteByte(c)
	}
	spelling.WriteString(name.String())
	if name.Len() == 0 {
		return envsubstReference{spelling: spelling.String()}, nil
	}
	if braced {
		if c, err := reader.ReadByte(); err != nil || c != '}' {
			if err == nil {
				_ = reader.UnreadByte()
			}
			return envsubstReference{spelling: spelling.String()}, nil
		}
		spelling.WriteByte('}')
	}
	return envsubstReference{name: name.String(), spelling: spelling.String()}, nil
}

// envsubstReferences lists SHELL-FORMAT's references in order, repeats kept.
func envsubstReferences(format string) []envsubstReference {
	var out []envsubstReference
	reader := bufio.NewReader(strings.NewReader(format))
	for {
		if _, err := reader.ReadString('$'); err != nil {
			return out
		}
		ref, _ := readReference(reader, int64(len(format)))
		if ref.name != "" {
			out = append(out, ref)
		}
	}
}

func nameByte(c byte, first bool) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || !first && c >= '0' && c <= '9'
}
