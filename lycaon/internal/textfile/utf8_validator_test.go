package textfile

import "testing"

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
