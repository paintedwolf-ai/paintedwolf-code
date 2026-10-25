package textutil

import "bytes"

// compareDecimal compares leading [blanks][-]digits[.digits] exactly, with no
// float rounding; text without a number is zero.
func compareDecimal(a, b []byte) int {
	an, ai, af := decimalParts(a)
	bn, bi, bf := decimalParts(b)
	if an != bn {
		if an {
			return -1
		}
		return 1
	}
	c := len(ai) - len(bi)
	if c == 0 {
		c = bytes.Compare(ai, bi)
	}
	if c == 0 {
		c = bytes.Compare(af, bf)
	}
	if c > 0 {
		c = 1
	} else if c < 0 {
		c = -1
	}
	if an {
		return -c
	}
	return c
}

// decimalParts returns the sign, integer digits without leading zeros and
// fraction digits without trailing zeros; zero is never negative.
func decimalParts(text []byte) (negative bool, integer, fraction []byte) {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i < len(text) && text[i] == '-' {
		negative = true
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	integer = bytes.TrimLeft(text[start:i], "0")
	if i < len(text) && text[i] == '.' {
		i++
		start = i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		fraction = bytes.TrimRight(text[start:i], "0")
	}
	if len(integer) == 0 && len(fraction) == 0 {
		negative = false
	}
	return negative, integer, fraction
}

// humanSuffixes ranks SI suffixes for -h; k is GNU's lowercase kilo.
var humanSuffixes = map[byte]int{'k': 1, 'K': 1, 'M': 2, 'G': 3, 'T': 4, 'P': 5, 'E': 6, 'Z': 7, 'Y': 8, 'R': 9, 'Q': 10}

// compareHuman orders -h numbers as GNU sort does: by sign, then by SI
// suffix, then by value, so 2K sorts after 1000 and before 1M.
func compareHuman(a, b []byte) int {
	an, _, _ := decimalParts(a)
	bn, _, _ := decimalParts(b)
	if an != bn {
		if an {
			return -1
		}
		return 1
	}
	c := humanRank(a) - humanRank(b)
	if c == 0 {
		return compareDecimal(a, b)
	}
	if c > 0 {
		c = 1
	} else {
		c = -1
	}
	if an {
		return -c
	}
	return c
}

// humanRank is the suffix directly after the number, or 0.
func humanRank(text []byte) int {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i < len(text) && text[i] == '-' {
		i++
	}
	digits := i
	for i < len(text) && (text[i] >= '0' && text[i] <= '9' || text[i] == '.') {
		i++
	}
	if i == digits || i >= len(text) {
		return 0
	}
	return humanSuffixes[text[i]]
}

// compareVersion is gnulib filevercmp, GNU sort -V: "." and ".." and other
// dot names first, then a comparison of names without their file suffixes
// (\.[A-Za-z~][A-Za-z0-9~]*)*$, then of whole names, where digit runs compare
// numerically and ~ sorts before everything, even the end.
func compareVersion(a, b []byte) int {
	switch {
	case bytes.Equal(a, b):
		return 0
	case len(a) == 0:
		return -1
	case len(b) == 0:
		return 1
	}
	if a[0] == '.' || b[0] == '.' {
		if a[0] != '.' {
			return 1
		}
		if b[0] != '.' {
			return -1
		}
		for _, special := range []string{".", ".."} {
			as, bs := string(a) == special, string(b) == special
			if as || bs {
				if as {
					return -1
				}
				return 1
			}
		}
	}
	ap, bp := versionPrefix(a), versionPrefix(b)
	if c := versionCompare(a[:ap], b[:bp]); c != 0 || (ap == len(a) && bp == len(b)) {
		return c
	}
	return versionCompare(a, b)
}

func versionPrefix(s []byte) int {
	prefix := 0
	for i := 0; i < len(s); {
		i++
		prefix = i
		for i+1 < len(s) && s[i] == '.' && (isAlpha(s[i+1]) || s[i+1] == '~') {
			for i += 2; i < len(s) && (isAlpha(s[i]) || isDigit(s[i]) || s[i] == '~'); i++ {
			}
		}
	}
	return prefix
}

func versionCompare(a, b []byte) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			if c := versionOrder(a, i) - versionOrder(b, j); c != 0 {
				return c
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		first := 0
		for i < len(a) && j < len(b) && isDigit(a[i]) && isDigit(b[j]) {
			if first == 0 {
				first = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if first != 0 {
			return first
		}
	}
	return 0
}

func versionOrder(s []byte, at int) int {
	if at >= len(s) {
		return -1
	}
	switch c := s[at]; {
	case isDigit(c):
		return 0
	case isAlpha(c):
		return int(c)
	case c == '~':
		return -2
	default:
		return int(c) + 256
	}
}

// compareFolded is byte order, with -f mapping lowercase ASCII to uppercase.
func compareFolded(a, b []byte, fold bool) int {
	if !fold {
		return bytes.Compare(a, b)
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if x, y := upper(a[i]), upper(b[i]); x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}
