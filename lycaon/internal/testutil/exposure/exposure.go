// Package exposure checks whether values a test supplied reach an output.
//
// Tests assert on the data they put in, never on wording the product puts around
// it, so copy can change without touching them. The package imports only the
// standard library, which lets any package's tests use it without an import cycle.
package exposure

import (
	"reflect"
	"slices"
	"testing"
)

// minValueBytes keeps values collected from a structure from matching by chance.
const minValueBytes = 4

// Scanner finds any of its values in one pass over an output. Each byte costs one
// table lookup however many values it holds, and a scan that finds nothing does
// not allocate. Build one Scanner to check several outputs.
type Scanner struct {
	values []string
	// next holds 256 transitions per state.
	next []int32
	// match is the value ending at each state, or -1.
	match []int32
	// chain is the nearest suffix state that also ends a value, or 0.
	chain []int32
}

// NewScanner compiles values, ignoring empty strings and duplicates.
func NewScanner(values ...string) *Scanner {
	s := &Scanner{}
	for _, v := range values {
		if v != "" && !slices.Contains(s.values, v) {
			s.values = append(s.values, v)
		}
	}
	s.next = make([]int32, 256)
	s.match = []int32{-1}
	for i, v := range s.values {
		state := int32(0)
		for j := 0; j < len(v); j++ {
			at := int(state)*256 + int(v[j])
			if s.next[at] == 0 {
				s.next[at] = int32(len(s.match))
				s.next = append(s.next, make([]int32, 256)...)
				s.match = append(s.match, -1)
			}
			state = s.next[at]
		}
		s.match[state] = int32(i)
	}
	s.link()
	return s
}

// link turns the trie into a complete transition table, breadth first.
func (s *Scanner) link() {
	fail := make([]int32, len(s.match))
	s.chain = make([]int32, len(s.match))
	queue := make([]int32, 0, len(s.match))
	for b := range 256 {
		if child := s.next[b]; child != 0 {
			queue = append(queue, child)
		}
	}
	for head := 0; head < len(queue); head++ {
		state := queue[head]
		if suffix := fail[state]; s.match[suffix] >= 0 {
			s.chain[state] = suffix
		} else {
			s.chain[state] = s.chain[suffix]
		}
		for b := range 256 {
			at := int(state)*256 + b
			via := s.next[int(fail[state])*256+b]
			if child := s.next[at]; child != 0 {
				fail[child] = via
				queue = append(queue, child)
			} else {
				s.next[at] = via
			}
		}
	}
}

// Found returns the values present in output, in the order they were given.
func (s *Scanner) Found(output string) []string {
	var seen []bool
	count := 0
	state := int32(0)
	for i := 0; i < len(output); i++ {
		state = s.next[int(state)*256+int(output[i])]
		for at := state; at != 0; at = s.chain[at] {
			value := s.match[at]
			if value < 0 {
				continue
			}
			if seen == nil {
				seen = make([]bool, len(s.values))
			}
			if !seen[value] {
				seen[value] = true
				count++
			}
		}
		if count == len(s.values) && count > 0 {
			break
		}
	}
	if count == 0 {
		return nil
	}
	found := make([]string, 0, count)
	for i, hit := range seen {
		if hit {
			found = append(found, s.values[i])
		}
	}
	return found
}

// Absent fails the test when output contains any of values.
func Absent(t testing.TB, output string, values ...string) {
	t.Helper()
	if found := NewScanner(values...).Found(output); len(found) > 0 {
		t.Fatalf("output exposes %q:\n%s", found, output)
	}
}

// Present fails the test unless output contains every one of values.
func Present(t testing.TB, output string, values ...string) {
	t.Helper()
	scanner := NewScanner(values...)
	found := scanner.Found(output)
	if len(found) == len(scanner.values) {
		return
	}
	var missing []string
	for _, v := range scanner.values {
		if !slices.Contains(found, v) {
			missing = append(missing, v)
		}
	}
	t.Fatalf("output lacks %q:\n%s", missing, output)
}

// Values reads the strings held anywhere inside inputs: struct fields, map keys
// and values, slices, pointers, interfaces and byte slices. Use it to name what
// would leak from the input itself. Strings under four bytes are left out.
func Values(inputs ...any) []string {
	var values []string
	visited := map[uintptr]bool{}
	for _, input := range inputs {
		collect(reflect.ValueOf(input), &values, visited)
	}
	return values
}

func collect(v reflect.Value, values *[]string, visited map[uintptr]bool) {
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); len(s) >= minValueBytes && !slices.Contains(*values, s) {
			*values = append(*values, s)
		}
	case reflect.Interface:
		collect(v.Elem(), values, visited)
	case reflect.Pointer:
		if v.IsNil() || visited[v.Pointer()] {
			return
		}
		visited[v.Pointer()] = true
		collect(v.Elem(), values, visited)
	case reflect.Struct:
		for i := range v.NumField() {
			collect(v.Field(i), values, visited)
		}
	case reflect.Map:
		if v.IsNil() || visited[v.Pointer()] {
			return
		}
		visited[v.Pointer()] = true
		for iter := v.MapRange(); iter.Next(); {
			collect(iter.Key(), values, visited)
			collect(iter.Value(), values, visited)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if len(v.Bytes()) >= minValueBytes {
				collect(reflect.ValueOf(string(v.Bytes())), values, visited)
			}
			return
		}
		fallthrough
	case reflect.Array:
		for i := range v.Len() {
			collect(v.Index(i), values, visited)
		}
	}
}
