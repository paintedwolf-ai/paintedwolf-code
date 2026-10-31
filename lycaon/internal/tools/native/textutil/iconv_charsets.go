package textutil

import (
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/ianaindex"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
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
