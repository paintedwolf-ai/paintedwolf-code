package visualscreen

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	"image/png"
	"io"

	_ "golang.org/x/image/webp"
)

// errAnimatedRaster reports a raster with frames after the first. Recognition
// reads one frame, so the others would reach the model unread.
var errAnimatedRaster = errors.New("animated image: frames after the first are not read")

// decodeRaster decodes the raster as a viewer displays it: every byte of pixel
// data, turned by its EXIF orientation. A container cut short fails here even
// where a lenient decoder would draw the rows it has. Recognition and redaction
// both read this raster, so recognized geometry is the geometry masked.
func decodeRaster(raw []byte) (image.Image, error) {
	var img image.Image
	var err error
	switch rasterMagic(raw) {
	case "png":
		if pngAnimated(raw) {
			return nil, errAnimatedRaster
		}
		img, err = png.Decode(bytes.NewReader(raw))
	case "gif":
		if err := singleFrameGIF(raw); err != nil {
			return nil, err
		}
		img, err = gif.Decode(bytes.NewReader(raw))
	default:
		img, _, err = image.Decode(bytes.NewReader(raw))
	}
	if err != nil {
		return nil, err
	}
	return orient(img, tiffOrientation(exifBlock(raw))), nil
}

// singleFrameGIF walks container blocks before any frame pixels are allocated.
// DecodeAll would allocate every frame before discovering that screening must refuse them.
func singleFrameGIF(raw []byte) error {
	if len(raw) < 13 {
		return io.ErrUnexpectedEOF
	}
	offset := 13
	if raw[10]&0x80 != 0 {
		offset += 3 * (1 << ((raw[10] & 7) + 1))
	}
	frames := 0
	for offset < len(raw) {
		switch raw[offset] {
		case 0x3b:
			if frames != 1 {
				return errors.New("gif has no frames")
			}
			return nil
		case 0x21:
			offset += 2 // Extension introducer and label; its data uses sub-blocks.
		case 0x2c:
			frames++
			if frames > 1 {
				return errAnimatedRaster
			}
			if offset+10 > len(raw) {
				return io.ErrUnexpectedEOF
			}
			packed := raw[offset+9]
			offset += 10
			if packed&0x80 != 0 {
				offset += 3 * (1 << ((packed & 7) + 1))
			}
			offset++ // LZW minimum code size.
		default:
			return errors.New("invalid gif block")
		}
		var err error
		offset, err = gifSubBlocksEnd(raw, offset)
		if err != nil {
			return err
		}
	}
	return io.ErrUnexpectedEOF
}

func gifSubBlocksEnd(raw []byte, offset int) (int, error) {
	for offset < len(raw) {
		n := int(raw[offset])
		offset++
		if n == 0 {
			return offset, nil
		}
		if offset+n > len(raw) {
			break
		}
		offset += n
	}
	return 0, io.ErrUnexpectedEOF
}

// pngAnimated reports an acTL chunk, which declares frames a still decoder
// skips. The chunk must precede the image data.
func pngAnimated(raw []byte) bool {
	for offset := int64(8); offset+8 <= int64(len(raw)); {
		switch string(raw[offset+4 : offset+8]) {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		offset += 12 + int64(binary.BigEndian.Uint32(raw[offset:]))
	}
	return false
}

// exifBlock returns the TIFF-structured EXIF data of a JPEG APP1 segment, a
// PNG eXIf chunk, or a WebP EXIF chunk; nil when there is none.
func exifBlock(raw []byte) []byte {
	const exifHeader = "Exif\x00\x00"
	switch rasterMagic(raw) {
	case "jpeg":
		for offset := 2; offset+4 <= len(raw) && raw[offset] == 0xff; {
			marker := raw[offset+1]
			if marker == 0xda || marker == 0xd9 {
				return nil
			}
			end := offset + 2 + int(binary.BigEndian.Uint16(raw[offset+2:]))
			if end > len(raw) {
				return nil
			}
			if segment := raw[offset+4 : end]; marker == 0xe1 && bytes.HasPrefix(segment, []byte(exifHeader)) {
				return segment[len(exifHeader):]
			}
			offset = end
		}
	case "png":
		for offset := int64(8); offset+8 <= int64(len(raw)); {
			n := int64(binary.BigEndian.Uint32(raw[offset:]))
			if offset+8+n > int64(len(raw)) {
				return nil
			}
			switch string(raw[offset+4 : offset+8]) {
			case "eXIf":
				return raw[offset+8 : offset+8+n]
			case "IEND":
				return nil
			}
			offset += 12 + n
		}
	case "webp":
		for offset := int64(12); offset+8 <= int64(len(raw)); {
			n := int64(binary.LittleEndian.Uint32(raw[offset+4:]))
			if offset+8+n > int64(len(raw)) {
				return nil
			}
			if string(raw[offset:offset+4]) == "EXIF" {
				return bytes.TrimPrefix(raw[offset+8:offset+8+n], []byte(exifHeader))
			}
			offset += 8 + n + n%2
		}
	}
	return nil
}

// tiffOrientation reads the orientation tag (1–8) from IFD0; 1 when it is
// absent or malformed.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int64(order.Uint32(tiff[4:]))
	if ifd+2 > int64(len(tiff)) {
		return 1
	}
	count := int64(order.Uint16(tiff[ifd:]))
	for entry := ifd + 2; entry < ifd+2+12*count && entry+12 <= int64(len(tiff)); entry += 12 {
		const orientationTag, shortType = 0x0112, 3
		if order.Uint16(tiff[entry:]) != orientationTag || order.Uint16(tiff[entry+2:]) != shortType {
			continue
		}
		if o := int(order.Uint16(tiff[entry+8:])); o >= 1 && o <= 8 {
			return o
		}
		return 1
	}
	return 1
}

// orient turns a raster by an EXIF orientation into the form a viewer shows.
func orient(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}
	src := asRGBA(img)
	w, h := src.Rect.Dx(), src.Rect.Dy()
	// Orientations 5–8 swap the edges.
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[dst.PixOffset(x, y):dst.PixOffset(x, y)+4], src.Pix[src.PixOffset(sx, sy):])
		}
	}
	return dst
}

// asRGBA returns the raster as premultiplied RGBA with its origin at zero.
func asRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Rect, img, b.Min, draw.Src)
	return rgba
}
