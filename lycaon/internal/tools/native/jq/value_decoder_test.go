package jq

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type eventMemoryFixture struct{ held, peak, maximum int64 }

func (m *eventMemoryFixture) Reserve(ctx context.Context, n int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n > m.maximum-m.held {
		return nil, errors.New("fixture memory exhausted")
	}
	m.held += n
	m.peak = max(m.peak, m.held)
	return func() { m.held -= n }, nil
}

func TestJSONEventMemoryFollowsCurrentPathAndScalar(t *testing.T) {
	for _, count := range []int{1000, 100000} {
		memory := &eventMemoryFixture{maximum: 128 << 10}
		stream := NewEventStream(t.Context(), strings.NewReader("["+strings.Repeat("1,", count-1)+"2]"), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, memory)
		events := 0
		for {
			_, err := stream.Decode()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read %d events: %v", events, err)
			}
			events++
		}
		stream.Close()
		if events != count+1 || memory.held != 0 || memory.peak > 64<<10 {
			t.Fatalf("count %d events %d memory %+v", count, events, memory)
		}
	}
}

func TestBoundedJSONValuesMatchCanonicalInput(t *testing.T) {
	for _, source := range []string{`{"a":{"x":1},"a":{"y":2}}`, `[1.0,2.50,1e3,-0,true,null]`, `{"b":[],"a":{}}`, `1 2 3`} {
		memory := &eventMemoryFixture{maximum: 1 << 20}
		bounded := NewBoundedValueStream(t.Context(), strings.NewReader(source), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, memory, 1<<20)
		plain := NewValueStream(strings.NewReader(source))
		for {
			got, e := bounded.Decode()
			want, w := plain.Decode()
			if e == io.EOF && w == io.EOF {
				break
			}
			if e != nil || w != nil {
				t.Fatalf("decode: %v %v", e, w)
			}
			a, _ := EncodeJSON(got, false)
			b, _ := EncodeJSON(want, false)
			if string(a) != string(b) {
				t.Fatalf("value %s want %s", a, b)
			}
		}
		bounded.Close()
		if memory.held != 0 {
			t.Fatalf("retained input %+v", memory)
		}
	}
}

func TestJSONDecoderBoundsAndCancellationPrecedeGrowth(t *testing.T) {
	for _, source := range []string{strings.Repeat("[", 129), `"` + strings.Repeat("x", 1025) + `"`} {
		memory := &eventMemoryFixture{maximum: 1 << 20}
		stream := NewEventStream(t.Context(), strings.NewReader(source), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, memory)
		_, err := stream.Decode()
		stream.Close()
		var limit *EventLimitError
		if !errors.As(err, &limit) || memory.held != 0 {
			t.Fatalf("bounded decode: %v memory %+v", err, memory)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stream := NewEventStream(ctx, strings.NewReader("[1]"), EventLimits{MaximumDepth: 128, MaximumScalarBytes: 1024}, nil)
	_, err := stream.Decode()
	stream.Close()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestJSONDecoderPreservesTerminalReaderErrors(t *testing.T) {
	for _, source := range []string{`{"x"`, `{"x":`, `[1,`} {
		for _, events := range []bool{false, true} {
			failure := errors.New("source unavailable")
			input := io.MultiReader(strings.NewReader(source), failingJSONReader{failure})
			memory := &eventMemoryFixture{maximum: 1 << 20}
			var decoder interface {
				Decode() (any, error)
				Close()
			}
			if events {
				decoder = NewEventStream(t.Context(), input, EventLimits{128, 1024}, memory)
			} else {
				decoder = NewBoundedValueStream(t.Context(), input, EventLimits{128, 1024}, memory, 1<<20)
			}
			var err error
			for err == nil {
				_, err = decoder.Decode()
			}
			if !errors.Is(err, failure) {
				t.Fatalf("source %s events %v: %v", source, events, err)
			}
			_, again := decoder.Decode()
			if !errors.Is(again, failure) {
				t.Fatalf("terminal error changed: %v", again)
			}
			decoder.Close()
			if memory.held != 0 {
				t.Fatalf("terminal read retained %+v", memory)
			}
		}
	}
}

type failingJSONReader struct{ failure error }

func (r failingJSONReader) Read([]byte) (int, error) { return 0, r.failure }

func TestJSONDecodersRequireRetainedAdmission(t *testing.T) {
	stream := NewEventStream(t.Context(), strings.NewReader("[1]"), EventLimits{128, 1024}, missingEventLease{})
	if _, err := stream.Decode(); err == nil {
		t.Fatal("decoded without admission")
	}
	if stream.InputLine() != 0 {
		t.Fatal("unread input has a line position")
	}
	stream.Close()
	stream.Close()
}

type missingEventLease struct{}

func (missingEventLease) Reserve(context.Context, int64) (func(), error) { return nil, nil }
