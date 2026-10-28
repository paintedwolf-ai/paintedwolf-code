package modelcall

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestWireCaptureBoundedAndDetached(t *testing.T) {
	var absent *WireCapture
	absent.Record(nil)
	if b, src, n := absent.Snapshot(); b != nil || src != nil || n != 0 {
		t.Fatal("nil capture changed")
	}
	c := &WireCapture{}
	body := []byte(`{"messages":[]}`)
	c.Record(body)
	body[0] = '!'
	got, _, n := c.Snapshot()
	if n != 1 || got[0] != '{' {
		t.Fatal("capture aliases caller")
	}
	got[0] = '?'
	again, _, _ := c.Snapshot()
	if again[0] != '{' {
		t.Fatal("snapshot aliases capture")
	}
	c.Record([]byte(`{}`))
	if b, _, n := c.Snapshot(); b != nil || n != 2 {
		t.Fatal("retry must discard ambiguous payload")
	}
	large := &WireCapture{}
	large.Record(bytes.Repeat([]byte("x"), MaxTrainingRequestBytes+1))
	if b, _, n := large.Snapshot(); b != nil || n != 1 {
		t.Fatal("oversize request retained")
	}
}

func TestDispatchRetainsTheDetachedSourceRequest(t *testing.T) {
	wire := &WireCapture{}
	capture := &RequestControlCapture{Wire: wire}
	req := CompletionRequest{Model: "m", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}, ControlCapture: capture}
	capture.RecordDispatch(req, []byte(`{"model":"m"}`))
	req.Messages[0].Content = "changed"
	_, source, n := wire.Snapshot()
	if n != 1 || source == nil || source.Messages[0].Content != "hi" || source.ControlCapture != nil {
		t.Fatalf("source = %+v", source)
	}
	capture.RecordDispatch(req, []byte(`{"model":"m"}`))
	if _, source, _ := wire.Snapshot(); source != nil {
		t.Fatal("a second dispatch must discard the source")
	}
}
