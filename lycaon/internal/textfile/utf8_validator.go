package textfile

import "unicode/utf8"

// UTF8Validator validates chunked UTF-8 text without NUL bytes.
type UTF8Validator struct {
	pending  [utf8.UTFMax]byte
	pendingN int
}

// Add validates one chunk. EOF rejects an incomplete final rune.
func (v *UTF8Validator) Add(raw []byte, eof bool) bool {
	if v.pendingN > 0 {
		for !utf8.FullRune(v.pending[:v.pendingN]) && len(raw) > 0 {
			v.pending[v.pendingN] = raw[0]
			v.pendingN++
			raw = raw[1:]
		}
		if !utf8.FullRune(v.pending[:v.pendingN]) {
			return !eof
		}
		if !validUTF8Rune(v.pending[:v.pendingN]) {
			return false
		}
		v.pendingN = 0
	}
	for len(raw) > 0 {
		if !utf8.FullRune(raw) {
			if eof {
				return false
			}
			v.pendingN = copy(v.pending[:], raw)
			return true
		}
		_, size := utf8.DecodeRune(raw)
		if !validUTF8Rune(raw[:size]) {
			return false
		}
		raw = raw[size:]
	}
	return true
}

func validUTF8Rune(raw []byte) bool {
	r, size := utf8.DecodeRune(raw)
	return !(r == utf8.RuneError && size == 1) && r != 0
}
