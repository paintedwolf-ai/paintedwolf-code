package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// joinSpec is one GNU join: join fields per file (zero-based), the field
// separator (-1 for blank runs), unpairable files to print, and the -o list.
type joinSpec struct {
	fold      bool
	field     [2]int
	separator int
	unpaired  [2]bool
	only      bool
	empty     []byte
	output    []joinOutput
}

// joinOutput is one -o entry: file 0 is the join field, otherwise file 1 or
// 2 and a zero-based field.
type joinOutput struct{ file, field int }

// runJoin joins two inputs sorted on their join fields: each pair of lines
// with equal keys prints the key, then FILE1's other fields, then FILE2's.
// -a N also prints unpairable lines of file N, -v N only those; -o selects
// fields with -e for missing ones. Without -t, blank runs separate fields and
// output uses a space.
func runJoin(inv *invocation) error {
	spec, err := parseJoin(inv)
	if err != nil {
		return err
	}
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
	j := joiner{inv: inv, spec: spec}
	for i, source := range sources {
		if j.readers[i], err = inv.records(source.reader, '\n'); err != nil {
			return err
		}
		if err := j.advance(i); err != nil {
			return err
		}
	}
	return j.merge()
}

func parseJoin(inv *invocation) (joinSpec, error) {
	spec := joinSpec{separator: -1, fold: inv.flag("-i")}
	for i, key := range []string{"-1", "-2"} {
		n, err := joinField(inv, key)
		if err != nil {
			return spec, err
		}
		spec.field[i] = n
	}
	if _, ok := inv.value("-j"); ok {
		n, err := joinField(inv, "-j")
		if err != nil {
			return spec, err
		}
		spec.field = [2]int{n, n}
	}
	if value, ok := inv.value("-t"); ok {
		if len(value.Value) != 1 {
			return spec, inv.usage(value, "a one-byte separator")
		}
		spec.separator = int(value.Value[0])
	}
	for _, key := range []string{"-a", "-v"} {
		for _, option := range inv.argv.Options(key) {
			n := option.Values[0].Value
			if n != "1" && n != "2" {
				return spec, inv.usage(option.Values[0], "file number 1 or 2")
			}
			spec.unpaired[n[0]-'1'] = true
			spec.only = spec.only || key == "-v"
		}
	}
	if value, ok := inv.value("-e"); ok {
		spec.empty = []byte(value.Value)
	}
	for _, option := range inv.argv.Options("-o") {
		for _, entry := range strings.FieldsFunc(option.Values[0].Value, func(r rune) bool { return r == ',' || r == ' ' }) {
			out, ok := parseJoinOutput(entry)
			if !ok {
				return spec, inv.usage(option.Values[0], "0 or FILE.FIELD entries such as 1.2,2.1")
			}
			spec.output = append(spec.output, out)
		}
	}
	return spec, nil
}

func joinField(inv *invocation, key string) (int, error) {
	value, ok := inv.value(key)
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(value.Value)
	if err != nil || n < 1 {
		return 0, inv.usage(value, "a field number from 1")
	}
	return n - 1, nil
}

func parseJoinOutput(entry string) (joinOutput, bool) {
	if entry == "0" {
		return joinOutput{}, true
	}
	file, field, ok := strings.Cut(entry, ".")
	n, err := strconv.Atoi(field)
	if !ok || (file != "1" && file != "2") || err != nil || n < 1 {
		return joinOutput{}, false
	}
	return joinOutput{file: int(file[0] - '0'), field: n - 1}, true
}

type joiner struct {
	inv     *invocation
	spec    joinSpec
	readers [2]*records
	current [2][][]byte
	live    [2]bool
}

func (j *joiner) advance(i int) error {
	record, err := j.readers[i].next()
	if errors.Is(err, io.EOF) {
		j.live[i] = false
		return nil
	}
	if err != nil {
		return err
	}
	if err := j.inv.reserve(int64(len(record))); err != nil {
		return err
	}
	j.live[i], j.current[i] = true, j.fields(bytes.Clone(record))
	return nil
}

func (j *joiner) fields(record []byte) [][]byte {
	if j.spec.separator >= 0 {
		return bytes.Split(record, []byte{byte(j.spec.separator)})
	}
	return bytes.Fields(record)
}

func (j *joiner) key(i int, fields [][]byte) []byte {
	if f := j.spec.field[i]; f < len(fields) {
		return fields[f]
	}
	return nil
}

// merge walks both inputs, pairing each group of equal keys.
func (j *joiner) merge() error {
	for j.live[0] && j.live[1] {
		c := compareFolded(j.key(0, j.current[0]), j.key(1, j.current[1]), j.spec.fold)
		if c != 0 {
			side := 0
			if c > 0 {
				side = 1
			}
			if err := j.unpairedLine(side, j.current[side]); err != nil {
				return err
			}
			if err := j.advance(side); err != nil {
				return err
			}
			continue
		}
		var groups [2][][][]byte
		for i := range groups {
			key := bytes.Clone(j.key(i, j.current[i]))
			for j.live[i] && compareFolded(j.key(i, j.current[i]), key, j.spec.fold) == 0 {
				groups[i] = append(groups[i], j.current[i])
				if err := j.advance(i); err != nil {
					return err
				}
			}
		}
		if j.spec.only {
			continue
		}
		for _, left := range groups[0] {
			for _, right := range groups[1] {
				if err := j.write([2][][]byte{left, right}); err != nil {
					return err
				}
			}
		}
	}
	for i := range j.live {
		for j.live[i] {
			if err := j.unpairedLine(i, j.current[i]); err != nil {
				return err
			}
			if err := j.advance(i); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *joiner) unpairedLine(side int, fields [][]byte) error {
	if !j.spec.unpaired[side] {
		return nil
	}
	var pair [2][][]byte
	pair[side] = fields
	return j.write(pair)
}

// write prints one joined or unpairable line; a nil side is missing.
func (j *joiner) write(pair [2][][]byte) error {
	separator := []byte{' '}
	if j.spec.separator >= 0 {
		separator = []byte{byte(j.spec.separator)}
	}
	side := 0
	if pair[0] == nil {
		side = 1
	}
	var out [][]byte
	if len(j.spec.output) > 0 {
		for _, entry := range j.spec.output {
			switch {
			case entry.file == 0:
				out = append(out, j.key(side, pair[side]))
			case pair[entry.file-1] != nil && entry.field < len(pair[entry.file-1]):
				out = append(out, pair[entry.file-1][entry.field])
			default:
				out = append(out, j.spec.empty)
			}
		}
	} else {
		out = append(out, j.key(side, pair[side]))
		for i, fields := range pair {
			for f, field := range fields {
				if f != j.spec.field[i] {
					out = append(out, field)
				}
			}
		}
	}
	return writeRecord(j.inv.out, bytes.Join(out, separator), '\n')
}
