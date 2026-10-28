package nativeutility

import (
	"bytes"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type activityWriter struct {
	state    *activityWrites
	activity *commandprogram.ExecutionWait
}
type activityWrites struct {
	bytes.Buffer
	activity *commandprogram.ExecutionWait
	mappings []toolcontract.StreamSourceMapping
}

func (w activityWriter) Write(data []byte) (int, error) {
	w.state.activity = w.activity
	return w.state.Write(data)
}
func (w activityWriter) WithActivity(activity *commandprogram.ExecutionWait) io.Writer {
	return activityWriter{state: w.state, activity: activity}
}
func (w activityWriter) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	w.state.mappings = append(w.state.mappings, mapping)
	return nil
}

func TestOutputActivityViewsKeepTheMeterMappingsAndRepeatedBinding(t *testing.T) {
	state := &activityWrites{}
	output := NewOutput(t.Context(), activityWriter{state: state}, "stream_bytes", 4)
	first, second := &commandprogram.ExecutionWait{}, &commandprogram.ExecutionWait{}
	bound := output.WithActivity(first)
	rebound, ok := bound.(interface {
		WithActivity(*commandprogram.ExecutionWait) io.Writer
	})
	if !ok {
		t.Fatal("output view lost activity rebinding")
	}
	final := rebound.WithActivity(second)
	if _, err := final.Write([]byte("data")); err != nil {
		t.Fatalf("write rebound output: %v", err)
	}
	if state.String() != "data" || output.Written() != 4 || state.activity != second {
		t.Fatalf("bytes=%q meter=%d activity=%p", state.String(), output.Written(), state.activity)
	}
	mapping := toolcontract.StreamSourceMapping{OutputStartByte: 0, OutputEndByte: 4}
	recorder, ok := final.(commandprogram.SourceMappingRecorder)
	if !ok {
		t.Fatal("output view lost source mappings")
	}
	if err := recorder.RecordSourceMapping(mapping); err != nil {
		t.Fatalf("record rebound source: %v", err)
	}
	if len(state.mappings) != 1 || state.mappings[0] != mapping {
		t.Fatalf("mappings %+v", state.mappings)
	}
	if _, err := final.Write([]byte("x")); err == nil || state.String() != "data" {
		t.Fatalf("shared meter bypassed: %q %v", state.String(), err)
	}
}
