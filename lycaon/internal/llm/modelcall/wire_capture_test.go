package modelcall

import (
	"bytes"
	"testing"
)

func TestWireCaptureBoundedAndDetached(t *testing.T) {
	var absent *WireCapture
	absent.Record(nil)
	if b, n := absent.Snapshot(); b != nil || n != 0 {
		t.Fatal("nil capture changed")
	}
	c := &WireCapture{}
	body := []byte(`{"messages":[]}`)
	c.Record(body)
	body[0] = '!'
	got, n := c.Snapshot()
	if n != 1 || got[0] != '{' {
		t.Fatal("capture aliases caller")
	}
	got[0] = '?'
	again, _ := c.Snapshot()
	if again[0] != '{' {
		t.Fatal("snapshot aliases capture")
	}
	c.Record([]byte(`{}`))
	if b, n := c.Snapshot(); b != nil || n != 2 {
		t.Fatal("retry must discard ambiguous payload")
	}
	large := &WireCapture{}
	large.Record(bytes.Repeat([]byte("x"), MaxTrainingRequestBytes+1))
	if b, n := large.Snapshot(); b != nil || n != 1 {
		t.Fatal("oversize request retained")
	}
}
