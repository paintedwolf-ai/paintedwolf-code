package textfile

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestUTF8Validator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		chunks [][]byte
		want   bool
	}{
		{name: "text controls", chunks: [][]byte{[]byte("a\x1b\f\xc2\x85")}, want: true},
		{name: "split rune", chunks: [][]byte{{'a', 0xe2}, {0x82, 0xac}}, want: true},
		{name: "NUL", chunks: [][]byte{[]byte("a\x00b")}, want: false},
		{name: "invalid", chunks: [][]byte{{0xff}}, want: false},
		{name: "incomplete", chunks: [][]byte{{0xe2, 0x82}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var validator UTF8Validator
			got := true
			for i, chunk := range tt.chunks {
				got = validator.Add(chunk, i == len(tt.chunks)-1)
				if !got {
					break
				}
			}
			if got != tt.want {
				t.Fatalf("Add() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUTF8StreamValidationAgreesWithWholeText(t *testing.T) {
	check := func(raw []byte) {
		t.Helper()
		want := utf8.Valid(raw) && !bytes.ContainsRune(raw, 0)
		for chunk := 1; chunk <= 5; chunk++ {
			var validator UTF8Validator
			got := true
			for start := 0; start < len(raw) && got; start += chunk {
				got = validator.Add(raw[start:min(start+chunk, len(raw))], false)
			}
			got = got && validator.Add(nil, true)
			if got != want {
				t.Fatalf("chunk=%d raw=%x accepted=%v, want %v", chunk, raw, got, want)
			}
		}
	}
	for first := 0; first <= 255; first++ {
		for second := 0; second <= 255; second++ {
			check([]byte{'a', 'b', byte(first), byte(second), 'c', 'd'})
		}
	}
	for _, raw := range [][]byte{
		{}, []byte("ordinary text controls\x1b\f"), []byte("éKΣİı𐐀\ufffd"),
		{0xf0, 0x90, 0x80, 0x80}, {0xf4, 0x8f, 0xbf, 0xbf}, {0xf4, 0x90, 0x80, 0x80},
		{0xed, 0x9f, 0xbf}, {0xed, 0xa0, 0x80}, {0xe0, 0x9f, 0xbf}, {0xe0, 0xa0, 0x80},
		{0xe2, 0x82}, {0xf0, 0x90, 0x80}, {0xf0, 0x80, 0x80, 0x80},
	} {
		check(raw)
	}
}
