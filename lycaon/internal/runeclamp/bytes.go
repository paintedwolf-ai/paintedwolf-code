package runeclamp

import "unicode/utf8"

// TruncatedSuffix is recognized by tool-output debug capture.
const TruncatedSuffix = "\n" + Marker + "[truncated]"

// CutBytes returns the longest UTF-8 prefix within maxBytes, without a marker.
func CutBytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// cutBytesTail returns the longest suffix of s that fits in maxBytes and starts
// on a rune boundary.
func cutBytesTail(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// ClampBytes keeps at most maxBytes plus Marker when truncated.
func ClampBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := CutBytes(s, maxBytes)
	if cut == "" {
		return ""
	}
	return cut + Marker
}

// ClampBytesTail keeps the end of s, marking what was dropped from the front.
func ClampBytesTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := cutBytesTail(s, maxBytes)
	if cut == "" {
		return ""
	}
	return Marker + cut
}

// ClampBytesMiddle keeps both ends and counts Marker within the byte budget.
func ClampBytesMiddle(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	budget := maxBytes - len(Marker)
	if budget <= 0 {
		return Marker
	}
	head := budget / 2
	return CutBytes(s, head) + Marker + cutBytesTail(s, budget-head)
}
