package jq

import (
	"io"
	"strings"
	"testing"
)

type eventReadGuard struct {
	source io.Reader
	read   int
}

func (r *eventReadGuard) Read(p []byte) (int, error) {
	n, e := r.source.Read(p)
	r.read += n
	return n, e
}
func TestJSONEventsDoNotReadTheCompleteDocument(t *testing.T) {
	input := &eventReadGuard{source: strings.NewReader("[0," + strings.Repeat("1,", 100000) + "2]")}
	stream := NewEventStream(t.Context(), input, EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1 << 20}, nil)
	defer stream.Close()
	event, err := stream.Decode()
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	raw, err := EncodeJSON(event, false)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	if string(raw) != "[[0],0]" {
		t.Fatalf("first event: %s", raw)
	}
	if input.read > 4096 {
		t.Fatalf("first event read %d bytes", input.read)
	}
}

func TestJSONEventsPreserveQueryEvents(t *testing.T) {
	for _, source := range []string{`[1,{"a":2}]`, `[]`, `{}`, `{"a":[],"b":{},"c":[[1],2]}`, `[null,true,false,2.0,1e3]`, `"a\\b"`, `1 2 {"x":3}`, `[[[[]]]]`} {
		t.Run(source, func(t *testing.T) {
			reference := NewValueStream(strings.NewReader(source))
			program, err := CompileProgram("tostream", ProgramOptions{})
			if err != nil {
				t.Fatalf("compile reference: %v", err)
			}
			var expected []string
			for {
				value, err := reference.Decode()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("decode reference: %v", err)
				}
				_, err = program.Run(t.Context(), value, func(v any) error { raw, e := EncodeJSON(v, false); expected = append(expected, string(raw)); return e })
				if err != nil {
					t.Fatalf("reference events: %v", err)
				}
			}
			actual := NewEventStream(t.Context(), strings.NewReader(source), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1 << 20}, nil)
			defer actual.Close()
			var got []string
			for {
				v, e := actual.Decode()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatalf("decode event: %v", e)
				}
				raw, e := EncodeJSON(v, false)
				if e != nil {
					t.Fatalf("encode event: %v", e)
				}
				got = append(got, string(raw))
			}
			if strings.Join(got, "\n") != strings.Join(expected, "\n") {
				t.Fatalf("events %q want %q", got, expected)
			}
		})
	}
}

func TestJSONEventsRefuseMalformedInput(t *testing.T) {
	for _, input := range []string{`[1,]`, `{"x":1,}`, `[1 2]`, `{1:2}`, `{"x" 1}`, `[`, `"unterminated`, `tru`} {
		s := NewEventStream(t.Context(), strings.NewReader(input), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, nil)
		var err error
		for err == nil {
			_, err = s.Decode()
		}
		s.Close()
		if err == io.EOF {
			t.Fatalf("malformed input accepted: %s", input)
		}
	}
}
