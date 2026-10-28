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
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// charset is one iconv code set: how its bytes decode and how runes encode.
// UTF-16 and UTF-32 decode big-endian unless a BOM says otherwise and
// encode with a BOM in little-endian order. A nil Encoding is strict 7-bit
// ASCII.
type charset struct {
	decode, encode encoding.Encoding
}

// charsetAliases are common spellings the IANA registry does not name.
var charsetAliases = map[string]string{
	"UTF8": "UTF-8", "LATIN1": "ISO-8859-1", "LATIN-1": "ISO-8859-1", "L1": "ISO-8859-1", "ISO88591": "ISO-8859-1",
	"CP1250": "windows-1250", "CP1251": "windows-1251", "CP1252": "windows-1252", "CP1253": "windows-1253",
	"CP1254": "windows-1254", "CP1255": "windows-1255", "CP1256": "windows-1256", "CP1257": "windows-1257", "CP1258": "windows-1258",
	"SJIS": "Shift_JIS", "EUCJP": "EUC-JP", "EUCKR": "EUC-KR", "MAC": "macintosh", "MACROMAN": "macintosh",
}

// charsetNames are the names iconv -l lists; each resolves through lookup.
var charsetNames = []string{
	"ASCII", "BIG5", "CP1250", "CP1251", "CP1252", "CP1253", "CP1254", "CP1255", "CP1256", "CP1257", "CP1258", "CP437", "CP850", "CP866",
	"EUC-JP", "EUC-KR", "GB18030", "GBK", "ISO-2022-JP", "ISO-8859-1", "ISO-8859-2", "ISO-8859-3", "ISO-8859-4", "ISO-8859-5",
	"ISO-8859-6", "ISO-8859-7", "ISO-8859-8", "ISO-8859-9", "ISO-8859-10", "ISO-8859-13", "ISO-8859-14", "ISO-8859-15", "ISO-8859-16",
	"KOI8-R", "KOI8-U", "MACINTOSH", "SHIFT_JIS", "UTF-16", "UTF-16BE", "UTF-16LE", "UTF-32", "UTF-32BE", "UTF-32LE", "UTF-8",
}

// lookupCharset resolves one -f or -t value: a code set name followed by
// optional //TRANSLIT and //IGNORE suffixes.
func lookupCharset(spec string) (set charset, translit, ignore, ok bool) {
	parts := strings.Split(spec, "//")
	for _, suffix := range parts[1:] {
		switch strings.ToUpper(suffix) {
		case "TRANSLIT":
			translit = true
		case "IGNORE":
			ignore = true
		case "":
		default:
			return set, false, false, false
		}
	}
	name := strings.ToUpper(parts[0])
	if name == "" {
		name = "UTF-8"
	}
	switch strings.ReplaceAll(name, "-", "") {
	case "ASCII", "USASCII", "ANSI_X3.41968", "646":
		return charset{}, translit, ignore, true
	case "UTF16":
		return charset{decode: xunicode.UTF16(xunicode.BigEndian, xunicode.UseBOM), encode: xunicode.UTF16(xunicode.LittleEndian, xunicode.UseBOM)}, translit, ignore, true
	case "UTF16LE":
		return pair(xunicode.UTF16(xunicode.LittleEndian, xunicode.IgnoreBOM)), translit, ignore, true
	case "UTF16BE":
		return pair(xunicode.UTF16(xunicode.BigEndian, xunicode.IgnoreBOM)), translit, ignore, true
	case "UTF32":
		return charset{decode: utf32.UTF32(utf32.BigEndian, utf32.UseBOM), encode: utf32.UTF32(utf32.LittleEndian, utf32.UseBOM)}, translit, ignore, true
	case "UTF32LE":
		return pair(utf32.UTF32(utf32.LittleEndian, utf32.IgnoreBOM)), translit, ignore, true
	case "UTF32BE":
		return pair(utf32.UTF32(utf32.BigEndian, utf32.IgnoreBOM)), translit, ignore, true
	}
	if alias, found := charsetAliases[name]; found {
		name = alias
	} else if rest, found := strings.CutPrefix(name, "ISO8859-"); found {
		name = "ISO-8859-" + rest
	}
	e, err := ianaindex.IANA.Encoding(name)
	if err != nil || e == nil {
		return set, false, false, false
	}
	return pair(e), translit, ignore, true
}

func pair(e encoding.Encoding) charset { return charset{decode: e, encode: e} }

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

// encodedReplacement is U+FFFD as the code set itself encodes it, so a real
// replacement character in the input is not mistaken for invalid bytes. Two
// are encoded and the second kept, which leaves out any byte order mark.
func encodedReplacement(e encoding.Encoding) []byte {
	one, errOne := e.NewEncoder().Bytes([]byte("\ufffd"))
	two, errTwo := e.NewEncoder().Bytes([]byte("\ufffd\ufffd"))
	if errOne != nil || errTwo != nil {
		return nil
	}
	return two[len(one):]
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
