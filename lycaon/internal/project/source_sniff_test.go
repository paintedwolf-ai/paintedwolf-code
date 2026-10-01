package project

import (
	"encoding/binary"
	"testing"
)

func TestSourceSniffDoesNotPromoteTextToImages(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"BM is a text prefix, not a bitmap\n",
		"<svgFactory>source code</svgFactory>",
		"<?xml version=\"1.0\"?><document><svg/></document>",
	} {
		mime, image := sniffSourceContent([]byte(content))
		if image {
			t.Errorf("text %q classified as image %q", content, mime)
		}
	}
}

func TestSourceSniffRecognizesImageHeaders(t *testing.T) {
	t.Parallel()
	bmp := make([]byte, 58)
	copy(bmp, "BM")
	binary.LittleEndian.PutUint32(bmp[2:6], uint32(len(bmp)))
	binary.LittleEndian.PutUint32(bmp[10:14], 54)
	binary.LittleEndian.PutUint32(bmp[14:18], 40)
	binary.LittleEndian.PutUint32(bmp[18:22], 1)
	binary.LittleEndian.PutUint32(bmp[22:26], 1)
	binary.LittleEndian.PutUint16(bmp[26:28], 1)
	binary.LittleEndian.PutUint16(bmp[28:30], 24)
	for _, tc := range []struct {
		name string
		raw  []byte
		mime string
	}{
		{"bitmap", bmp, "image/bmp"},
		{"svg", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), "image/svg+xml"},
		{"svg comment", []byte("\xef\xbb\xbf<!-- vector -->\n<svg/>"), "image/svg+xml"},
		{"icon", []byte{0, 0, 1, 0, 1, 0}, "image/x-icon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mime, image := sniffSourceContent(tc.raw)
			if !image || mime != tc.mime {
				t.Fatalf("mime=%q image=%v, want %q image", mime, image, tc.mime)
			}
		})
	}
}
