package visualscreen

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecodeRasterTruncatedJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 5), G: uint8(y * 5), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		testutil.FailErr(t, "jpeg.Encode failed", err)
	}
	raw := buf.Bytes()
	truncated := raw[:len(raw)/2]
	_, err := decodeRaster(truncated)
	if err == nil {
		t.Fatalf("decodeRaster did not fail on truncated JPEG")
	}
}

func TestDecodeRasterTruncatedPNG(t *testing.T) {
	raw := whitePNG(t, 50, 50)
	truncated := raw[:len(raw)/2]
	_, err := decodeRaster(truncated)
	if err == nil {
		t.Fatalf("decodeRaster did not fail on truncated PNG")
	}
}

func TestDecodeRasterTruncatedGIF(t *testing.T) {
	palette := color.Palette{color.White, color.Black}
	palImg := image.NewPaletted(image.Rect(0, 0, 50, 50), palette)
	var buf bytes.Buffer
	if err := gif.Encode(&buf, palImg, nil); err != nil {
		testutil.FailErr(t, "gif.Encode failed", err)
	}
	raw := buf.Bytes()
	truncated := raw[:len(raw)/2]
	_, err := decodeRaster(truncated)
	if err == nil {
		t.Fatalf("decodeRaster did not fail on truncated GIF")
	}
}

func TestDecodeRasterAnimatedGIF(t *testing.T) {
	palette := color.Palette{color.White, color.Black}
	frame1 := image.NewPaletted(image.Rect(0, 0, 10, 10), palette)
	frame2 := image.NewPaletted(image.Rect(0, 0, 10, 10), palette)
	g := &gif.GIF{
		Image: []*image.Paletted{frame1, frame2},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		testutil.FailErr(t, "gif.EncodeAll failed", err)
	}
	_, err := decodeRaster(buf.Bytes())
	if !errors.Is(err, errAnimatedRaster) {
		t.Fatalf("expected errAnimatedRaster, got %v", err)
	}
}

func TestDecodeRasterEmptyGIF(t *testing.T) {
	// A valid GIF header, screen descriptor, and trailer with no frames.
	emptyGIF := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x3b")
	_, err := decodeRaster(emptyGIF)
	if err == nil {
		t.Fatal("expected error decoding empty GIF")
	}
}

func apngBytes(t *testing.T) []byte {
	t.Helper()
	raw := whitePNG(t, 2, 2)
	idat := bytes.Index(raw, []byte("IDAT")) - 4
	var out bytes.Buffer
	out.Write(raw[:idat])
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], 8)
	out.Write(length[:])
	out.WriteString("acTL")
	payload := []byte("\x00\x00\x00\x02\x00\x00\x00\x00")
	out.Write(payload)
	crc := crc32.NewIEEE()
	crc.Write([]byte("acTL"))
	crc.Write(payload)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	out.Write(sum[:])
	out.Write(raw[idat:])
	return out.Bytes()
}

func TestDecodeRasterAnimatedPNG(t *testing.T) {
	raw := apngBytes(t)
	_, err := decodeRaster(raw)
	if !errors.Is(err, errAnimatedRaster) {
		t.Fatalf("expected errAnimatedRaster, got %v", err)
	}
}

func TestOrientTransformations(t *testing.T) {
	// 2x3 image with distinct pixel at (0, 0)
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	for orientation := 1; orientation <= 8; orientation++ {
		turned := orient(src, orientation)
		b := turned.Bounds()
		if orientation >= 5 {
			if b.Dx() != 3 || b.Dy() != 2 {
				t.Fatalf("orientation %d bounds = %v, want 3x2", orientation, b)
			}
		} else {
			if b.Dx() != 2 || b.Dy() != 3 {
				t.Fatalf("orientation %d bounds = %v, want 2x3", orientation, b)
			}
		}
	}
}

func makeTIFF(order binary.ByteOrder, orientation uint16) []byte {
	buf := new(bytes.Buffer)
	if order == binary.LittleEndian {
		buf.WriteString("II")
	} else {
		buf.WriteString("MM")
	}
	_ = binary.Write(buf, order, uint16(42))
	_ = binary.Write(buf, order, uint32(8)) // IFD offset
	_ = binary.Write(buf, order, uint16(1)) // 1 entry
	// Entry: Tag 0x0112, Type 3 (SHORT), Count 1, Value
	_ = binary.Write(buf, order, uint16(0x0112))
	_ = binary.Write(buf, order, uint16(3))
	_ = binary.Write(buf, order, uint32(1))
	_ = binary.Write(buf, order, orientation)
	_ = binary.Write(buf, order, uint16(0)) // padding
	return buf.Bytes()
}

func TestTIFFOrientation(t *testing.T) {
	for orientation := uint16(1); orientation <= 8; orientation++ {
		tiffLE := makeTIFF(binary.LittleEndian, orientation)
		if got := tiffOrientation(tiffLE); got != int(orientation) {
			t.Fatalf("LE orientation = %d, want %d", got, orientation)
		}
		tiffBE := makeTIFF(binary.BigEndian, orientation)
		if got := tiffOrientation(tiffBE); got != int(orientation) {
			t.Fatalf("BE orientation = %d, want %d", got, orientation)
		}
	}
	if got := tiffOrientation([]byte("short")); got != 1 {
		t.Fatalf("short tiff = %d, want 1", got)
	}
}

func TestDecodeRasterRejectsSecondGIFFrameBeforeDecodingItsPixels(t *testing.T) {
	var buf bytes.Buffer
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.White, color.Black})
	if err := gif.Encode(&buf, frame, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	raw := buf.Bytes()
	// A second image introducer is sufficient to refuse animation. Never decode
	// or allocate its pixels, even when the remainder of that frame is incomplete.
	animated := append(append([]byte(nil), raw[:len(raw)-1]...), 0x2c)
	if _, err := decodeRaster(animated); !errors.Is(err, errAnimatedRaster) {
		t.Fatalf("second frame err=%v", err)
	}

	// Image-separator bytes inside an extension are data, not additional frames.
	commented := append(append([]byte(nil), raw[:len(raw)-1]...), 0x21, 0xfe, 2, 0x2c, 0x2c, 0, 0x3b)
	if _, err := decodeRaster(commented); err != nil {
		t.Fatalf("commented gif: %v", err)
	}
	for _, bad := range [][]byte{raw[:len(raw)-1], append(append([]byte(nil), raw[:len(raw)-1]...), 0x21), append(append([]byte(nil), raw[:len(raw)-1]...), 0x7f)} {
		if _, err := decodeRaster(bad); err == nil {
			t.Fatal("malformed gif was accepted")
		}
	}
}
