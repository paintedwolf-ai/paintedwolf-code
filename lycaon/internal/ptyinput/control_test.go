package ptyinput

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExpandControlInputLiteralAndTokens(t *testing.T) {
	got, err := ExpandControlInput("y{Enter}{Ctrl-C}")
	testutil.FailErr(t, "ExpandControlInput", err)
	want := append([]byte{'y', '\r'}, 0x03)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandControlInputArrows(t *testing.T) {
	got, err := ExpandControlInput("{Up}{Down}{Left}{Right}")
	testutil.FailErr(t, "ExpandControlInput arrows", err)
	want := []byte{0x1b, '[', 'A', 0x1b, '[', 'B', 0x1b, '[', 'D', 0x1b, '[', 'C'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandControlInputUnknown(t *testing.T) {
	_, err := ExpandControlInput("{F1}")
	if err == nil {
		t.Fatal("expected unknown token error")
	}
}

func TestQuotedTerminalTextPreservesEveryByte(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	for _, value := range []string{string(all), "{Ctrl-C}", "{{ code.value }}", "{LeftBrace}{Enter}", "{unterminated", "雪{a}"} {
		got, err := ExpandControlInput(QuoteLiteral(value))
		testutil.FailErr(t, "expand quoted terminal text", err)
		if !bytes.Equal(got, []byte(value)) {
			t.Fatalf("literal terminal bytes changed: got %x, want %x", got, []byte(value))
		}
	}
}
