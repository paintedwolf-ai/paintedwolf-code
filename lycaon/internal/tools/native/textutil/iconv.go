package textutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// errIllegal and errIncomplete stop a conversion with a diagnostic.
var (
	errIllegal    = errors.New("illegal input sequence")
	errIncomplete = errors.New("incomplete character or shift sequence at end of buffer")
)

// conversion converts one input stream character by character so every
// invalid or unencodable character has its exact byte position.
type conversion struct {
	from, to         charset
	translit, ignore bool
	decoder          transform.Transformer
	encoder          transform.Transformer
	legitReplacement []byte
	omitted          bool
	// decoded and encoded each hold one character's bytes.
	decoded, encoded [64]byte
}

func newConversion(from, to charset, translit, ignore bool) *conversion {
	c := &conversion{from: from, to: to, translit: translit, ignore: ignore}
	if from.decode != nil {
		c.decoder = from.decode.NewDecoder()
		c.legitReplacement = encodedReplacement(from.decode)
	}
	if to.encode != nil {
		c.encoder = to.encode.NewEncoder()
	}
	return c
}

// decodeOne reads one character at the start of src, which holds eight
// bytes unless the input ends sooner (atEOF). It returns the character's
// UTF-8 bytes (possibly none, for a shift sequence) and the bytes it
// consumed. A sequence still incomplete at the input's end is errIncomplete;
// one no decoder completes within eight bytes is illegal.
func (c *conversion) decodeOne(src []byte, atEOF bool) ([]byte, int, error) {
	if c.decoder == nil {
		if src[0] >= 0x80 {
			return nil, 1, errIllegal
		}
		return src[:1], 1, nil
	}
	for window := 1; window <= min(len(src), 8); window++ {
		nDst, nSrc, err := c.decoder.Transform(c.decoded[:], src[:window], false)
		if errors.Is(err, transform.ErrShortSrc) && nSrc == 0 {
			continue
		}
		if err != nil && !errors.Is(err, transform.ErrShortSrc) {
			return nil, max(nSrc, 1), errIllegal
		}
		out := c.decoded[:nDst]
		if bytes.Contains(out, []byte("\ufffd")) && !bytes.Equal(src[:nSrc], c.legitReplacement) {
			return nil, nSrc, errIllegal
		}
		return out, nSrc, nil
	}
	if atEOF {
		return nil, len(src), errIncomplete
	}
	return nil, 1, errIllegal
}

// encodeOne writes one character's encoding, or reports it unencodable.
func (c *conversion) encodeOne(out *bytes.Buffer, char []byte) bool {
	if c.encoder == nil {
		r, _ := utf8.DecodeRune(char)
		if r >= 0x80 {
			return false
		}
		out.Write(char)
		return true
	}
	nDst, _, err := c.encoder.Transform(c.encoded[:], char, false)
	if err != nil {
		return false
	}
	out.Write(c.encoded[:nDst])
	return true
}

// emit encodes one decoded character, transliterating or omitting it when
// the target cannot represent it.
func (c *conversion) emit(out *bytes.Buffer, char []byte) bool {
	if len(char) == 0 || c.encodeOne(out, char) {
		return true
	}
	if c.translit {
		r, _ := utf8.DecodeRune(char)
		var approximate bytes.Buffer
		ok := true
		for _, a := range transliteration(r) {
			if !c.encodeOne(&approximate, []byte(string(a))) {
				ok = false
				break
			}
		}
		if !ok {
			approximate.Reset()
			c.encodeOne(&approximate, []byte("?"))
		}
		out.Write(approximate.Bytes())
		return true
	}
	if c.ignore {
		c.omitted = true
		return true
	}
	return false
}

// transliterationTable covers characters compatibility decomposition leaves
// unencodable, mostly punctuation.
var transliterationTable = map[rune]string{
	'‘': "'", '’': "'", '‚': "'", '‛': "'", '“': `"`, '”': `"`, '„': `"`,
	'–': "-", '—': "-", '−': "-", '…': "...", '€': "EUR", ' ': " ", 'ß': "ss",
	'æ': "ae", 'Æ': "AE", 'œ': "oe", 'Œ': "OE", 'ø': "o", 'Ø': "O", 'ł': "l", 'Ł': "L",
}

// transliteration approximates r: a table entry, or its compatibility
// decomposition without combining marks.
func transliteration(r rune) string {
	if s, ok := transliterationTable[r]; ok {
		return s
	}
	var out strings.Builder
	for _, d := range norm.NFKD.String(string(r)) {
		if !unicode.Is(unicode.Mn, d) {
			out.WriteRune(d)
		}
	}
	return out.String()
}

// run converts one input to w and reports the byte position it reached.
// A stop leaves the converted prefix written.
func (c *conversion) run(ctx context.Context, in io.Reader, w io.Writer) (int64, error) {
	reader := bufio.NewReaderSize(in, iconvChunk)
	var out bytes.Buffer
	var position int64
	for {
		src, err := reader.Peek(8)
		if err != nil && !errors.Is(err, io.EOF) {
			return position, err
		}
		if len(src) == 0 {
			break
		}
		char, n, err := c.decodeOne(src, len(src) < 8)
		switch {
		case errors.Is(err, errIllegal) && c.ignore:
			c.omitted, err = true, nil
		case err == nil && !c.emit(&out, char):
			err = errIllegal
		}
		if err != nil {
			_, _ = w.Write(out.Bytes())
			return position, err
		}
		position += int64(n)
		if _, err := reader.Discard(n); err != nil {
			return position, err
		}
		if out.Len() >= iconvChunk {
			if err := ctx.Err(); err != nil {
				return position, err
			}
			if _, err := w.Write(out.Bytes()); err != nil {
				return position, err
			}
			out.Reset()
		}
	}
	if c.encoder != nil {
		nDst, _, _ := c.encoder.Transform(c.encoded[:], nil, true)
		out.Write(c.encoded[:nDst])
	}
	_, err := w.Write(out.Bytes())
	return position, err
}

// iconvChunk is the read buffer and output batch size.
const iconvChunk = 32 << 10

func runIconv(inv *invocation) error {
	if inv.flag("-l") {
		_, err := io.WriteString(inv.out, strings.Join(charsetNames, "\n")+"\n")
		return err
	}
	fromSpec, toSpec := "UTF-8", "UTF-8"
	if field, ok := inv.value("-f"); ok {
		fromSpec = field.Value
	}
	if field, ok := inv.value("-t"); ok {
		toSpec = field.Value
	}
	from, _, _, fromOK := lookupCharset(fromSpec)
	to, translit, ignore, toOK := lookupCharset(toSpec)
	if !fromOK || !toOK {
		return inv.iconvUnsupported(fromSpec, toSpec, fromOK, toOK)
	}
	ignore = ignore || inv.flag("-c")
	if err := inv.reserve(128 << 10); err != nil {
		return err
	}
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	destination, staged := inv.value("-o")
	if !staged {
		return inv.convertSources(sources, from, to, translit, ignore, inv.out)
	}
	// -o publishes only a complete conversion, like a redirect.
	stage, err := inv.openDestination(destination)
	if err != nil {
		return err
	}
	output := nativeutility.NewOutput(inv.ctx, stage, "stream_bytes", inv.limit("stream_bytes"))
	if err := inv.convertSources(sources, from, to, translit, ignore, output); err != nil || inv.status != 0 {
		return errors.Join(err, stage.Abandon(context.WithoutCancel(inv.ctx)))
	}
	return inv.settleStage(stage, output.Flush())
}

// convertSources converts each input in turn; a diagnostic ends the run
// with status 1, and -c's omissions also leave status 1.
func (inv *invocation) convertSources(sources []source, from, to charset, translit, ignore bool, w io.Writer) error {
	for _, src := range sources {
		c := newConversion(from, to, translit, ignore)
		position, err := c.run(inv.ctx, src.reader, w)
		switch {
		case errors.Is(err, errIllegal):
			fmt.Fprintf(&inv.errors, "iconv: illegal input sequence at position %d\n", position)
			inv.status = 1
			return nil
		case errors.Is(err, errIncomplete):
			inv.errors.WriteString("iconv: incomplete character or shift sequence at end of buffer\n")
			inv.status = 1
			return nil
		case err != nil:
			return err
		}
		if c.omitted {
			inv.status = 1
		}
	}
	return nil
}

// iconvUnsupported refuses an unknown code set with status 1.
func (inv *invocation) iconvUnsupported(from, to string, fromOK, toOK bool) error {
	switch {
	case !fromOK && !toOK:
		fmt.Fprintf(&inv.errors, "iconv: conversions from `%s' and to `%s' are not supported\n", from, to)
	case !fromOK:
		fmt.Fprintf(&inv.errors, "iconv: conversion from `%s' is not supported\n", from)
	default:
		fmt.Fprintf(&inv.errors, "iconv: conversion to `%s' is not supported\n", to)
	}
	inv.errors.WriteString("Try 'iconv --help' or 'iconv --usage' for more information.\n")
	inv.status = 1
	return nil
}
