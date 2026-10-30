package sourcecatalog

import (
	"bytes"
	"testing"
)

func BenchmarkContentLearning(b *testing.B) {
	for _, source := range []struct {
		name  string
		bytes []byte
	}{
		{"ascii", bytes.Repeat([]byte("package main\nfunc lookup(value string) bool { return value == \"token\" }\n"), 1024)},
		{"unicode", bytes.Repeat([]byte("package main // Kelvin Σigma İstanbul élan\n"), 1024)},
	} {
		b.Run(source.name, func(b *testing.B) {
			b.SetBytes(int64(len(source.bytes)))
			b.ReportAllocs()
			for b.Loop() {
				content := newLiteralContent(int64(len(source.bytes)))
				if _, err := content.Write(source.bytes); err != nil {
					b.Fatalf("construct content filter: %v", err)
				}
				if _, err := content.finish(); err != nil {
					b.Fatalf("construct content filter: %v", err)
				}
			}
		})
	}
}
