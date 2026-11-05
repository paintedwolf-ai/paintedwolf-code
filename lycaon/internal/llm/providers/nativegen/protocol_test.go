package nativegen

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type fragmentWriter struct{ bytes.Buffer }

func (w *fragmentWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

func TestProtocolFramesHandleFragmentationAndBounds(t *testing.T) {
	payload := []byte(`{"id":1,"message":"é"}`)
	writer := new(fragmentWriter)
	if err := writeProtocolFrame(writer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readProtocolFrame(&writer.Buffer)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	for _, size := range []uint32{0, maxFrameBytes + 1, ^uint32(0)} {
		var header [4]byte
		binary.LittleEndian.PutUint32(header[:], size)
		if _, err := readProtocolFrame(bytes.NewReader(header[:])); err == nil {
			t.Fatalf("accepted frame length %d", size)
		}
	}
	if _, err := readProtocolFrame(bytes.NewReader([]byte{3, 0, 0, 0, 'x'})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame = %v", err)
	}
	if err := writeProtocolFrame(io.Discard, nil); err == nil {
		t.Fatal("accepted an empty frame")
	}
}

func TestProtocolResponseRejectsAmbiguousOrStaleStates(t *testing.T) {
	for _, payload := range []string{
		`{"status":"ok","id":2,"result":{"kind":"loaded"}}`,
		`{"status":"ok","id":1,"result":{"kind":"unloaded"}}`,
		`{"status":"ok","id":1,"code":"capacity","result":{"kind":"loaded"}}`,
		`{"status":"error","id":1,"code":"capacity","result":{"kind":"loaded"}}`,
		`{"status":"error","id":1,"code":"unknown"}`,
		`{"status":"ok","id":1,"result":{"kind":"loaded"},"extra":true}`,
		`{"status":"ok","id":1,"result":{"kind":"loaded"}} {}`,
	} {
		if _, err := decodeResponse([]byte(payload), 1, "loaded"); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	_, err := decodeResponse([]byte(`{"status":"error","id":1,"code":"capacity","message":"no memory"}`), 1, "loaded")
	var service *ServiceError
	if !errors.As(err, &service) || service.Code != "capacity" {
		t.Fatalf("lost structured error: %v", err)
	}
	result, err := decodeResponse([]byte(`{"status":"ok","id":1,"result":{"kind":"loaded","extra":true}}`), 1, "loaded")
	if err != nil {
		t.Fatal(err)
	}
	var loaded struct {
		Kind string `json:"kind"`
	}
	if err := decodeStrict(result, &loaded); err == nil {
		t.Fatal("accepted an undeclared result field")
	}
}
