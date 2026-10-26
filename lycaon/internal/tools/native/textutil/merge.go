package textutil

import (
	"errors"
	"io"
)

// runPaste joins corresponding lines of each input with the cycling -d list
// (default TAB; \n \t \\ and \0, which is no delimiter), or with -s each
// input's lines into one line. Repeated - operands read successive lines of
// standard input.
func runPaste(inv *invocation) error {
	delimiters := [][]byte{{'\t'}}
	if value, ok := inv.value("-d"); ok {
		delimiters = pasteDelimiters(value.Value)
	}
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	readers := make([]*records, len(sources))
	defer closeRecords(readers)
	var stdin *records
	for i, source := range sources {
		if source.name == "-" && stdin != nil {
			readers[i] = stdin
			continue
		}
		if readers[i], err = inv.records(source.reader, '\n'); err != nil {
			return err
		}
		if source.name == "-" {
			stdin = readers[i]
		}
	}
	if inv.flag("-s") {
		return pasteSerial(inv, readers, delimiters)
	}
	for {
		var line []byte
		live := false
		for i, reader := range readers {
			if i > 0 {
				line = append(line, delimiters[(i-1)%len(delimiters)]...)
			}
			record, err := reader.next()
			if errors.Is(err, io.EOF) {
				continue
			}
			if err != nil {
				return err
			}
			live = true
			line = append(line, record...)
		}
		if !live {
			return nil
		}
		if err := writeRecord(inv.out, line, '\n'); err != nil {
			return err
		}
	}
}

func pasteSerial(inv *invocation, readers []*records, delimiters [][]byte) error {
	for _, reader := range readers {
		var line []byte
		for n := 0; ; n++ {
			record, err := reader.next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if n > 0 {
				line = append(line, delimiters[(n-1)%len(delimiters)]...)
			}
			line = append(line, record...)
		}
		if err := writeRecord(inv.out, line, '\n'); err != nil {
			return err
		}
	}
	return nil
}

func pasteDelimiters(list string) [][]byte {
	var out [][]byte
	for i := 0; i < len(list); i++ {
		c := list[i]
		if c == '\\' && i+1 < len(list) {
			i++
			switch list[i] {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case '0':
				out = append(out, nil)
				continue
			default:
				c = list[i]
			}
		}
		out = append(out, []byte{c})
	}
	if len(out) == 0 {
		return [][]byte{nil}
	}
	return out
}

// runComm compares two sorted inputs line by line: column 1 lines only in
// FILE1, column 2 only in FILE2, column 3 in both, each indented by a TAB per
// earlier column shown; -1, -2 and -3 suppress columns and -i compares
// ASCII case-insensitively. - is standard input.
func runComm(inv *invocation) error {
	operands := inv.operands()
	if len(operands) != 2 {
		return inv.usage(inv.commandWord(), "FILE1 and FILE2")
	}
	sources, err := inv.openInputs(operands)
	if err != nil {
		return err
	}
	defer closeSources(sources)
	if len(sources) != 2 {
		inv.status = 1
		return nil
	}
	var readers [2]*records
	defer closeRecords(readers[:])
	for i, source := range sources {
		if readers[i], err = inv.records(source.reader, '\n'); err != nil {
			return err
		}
	}
	show := [3]bool{!inv.flag("-1"), !inv.flag("-2"), !inv.flag("-3")}
	fold := inv.flag("-i")
	prefix := [3]string{"", "", ""}
	if show[0] {
		prefix[1], prefix[2] = "\t", "\t"
	}
	if show[1] {
		prefix[2] += "\t"
	}
	var current [2][]byte
	var live [2]bool
	advance := func(i int) error {
		record, err := readers[i].next()
		if errors.Is(err, io.EOF) {
			live[i] = false
			return nil
		}
		live[i], current[i] = true, append(current[i][:0], record...)
		return err
	}
	for i := range readers {
		if err := advance(i); err != nil {
			return err
		}
	}
	for live[0] || live[1] {
		column := 2
		switch {
		case !live[1] || (live[0] && compareFolded(current[0], current[1], fold) < 0):
			column = 0
		case !live[0] || compareFolded(current[0], current[1], fold) > 0:
			column = 1
		}
		if show[column] {
			line := append([]byte(prefix[column]), current[column%2]...)
			if err := writeRecord(inv.out, line, '\n'); err != nil {
				return err
			}
		}
		for i := range readers {
			if column == i || column == 2 {
				if err := advance(i); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
