package datetext

import (
	"strconv"
	"strings"
	"time"
)

// Default is date's output format with no +FORMAT.
const Default = "%a %b %e %H:%M:%S %Z %Y"

// Format renders t with a +FORMAT string in the C locale. A conversion takes
// optional flags (- no padding, _ space padding, 0 zero padding, ^ upper case,
// # opposite case), a field width and, for %z, up to three colons. Unknown
// conversions are copied as written.
func Format(t time.Time, format string) string {
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			out.WriteByte(format[i])
			continue
		}
		start := i
		i++
		var pad byte
		upper, swap := false, false
		for ; i < len(format); i++ {
			switch format[i] {
			case '-', '_', '0':
				pad = format[i]
				continue
			case '^':
				upper = true
				continue
			case '#':
				swap = true
				continue
			}
			break
		}
		width := -1
		for ; i < len(format) && isDigit(format[i]); i++ {
			width = max(width, 0)*10 + int(format[i]-'0')
		}
		colons := 0
		for ; i < len(format) && format[i] == ':'; i++ {
			colons++
		}
		if i >= len(format) {
			out.WriteString(format[start:])
			break
		}
		text, ok := conversion(t, format[i], colons, width)
		if !ok {
			out.WriteString(format[start : i+1])
			continue
		}
		out.WriteString(field(text, pad, width, upper, swap, format[i]))
	}
	return out.String()
}

// conv is one rendered conversion and its default padding.
type conv struct {
	text  string
	fill  byte // '0', ' ' or 0 for text
	width int
}

func conversion(t time.Time, c byte, colons, width int) (conv, bool) {
	year, month, day := t.Date()
	hour, minute, sec := t.Clock()
	num := func(n, w int) conv { return conv{text: strconv.Itoa(n), fill: '0', width: w} }
	spaced := func(n, w int) conv { return conv{text: strconv.Itoa(n), fill: ' ', width: w} }
	text := func(s string) conv { return conv{text: s} }
	if colons > 0 && c != 'z' {
		return conv{}, false
	}
	switch c {
	case '%':
		return text("%"), true
	case 'a':
		return text(t.Weekday().String()[:3]), true
	case 'A':
		return text(t.Weekday().String()), true
	case 'b', 'h':
		return text(month.String()[:3]), true
	case 'B':
		return text(month.String()), true
	case 'c':
		return text(Format(t, "%a %b %e %H:%M:%S %Y")), true
	case 'C':
		return num(year/100, 2), true
	case 'd':
		return num(day, 2), true
	case 'D', 'x':
		return text(Format(t, "%m/%d/%y")), true
	case 'e':
		return spaced(day, 2), true
	case 'F':
		return text(Format(t, "%Y-%m-%d")), true
	case 'g':
		y, _ := t.ISOWeek()
		return num(y%100, 2), true
	case 'G':
		y, _ := t.ISOWeek()
		return num(y, 4), true
	case 'H':
		return num(hour, 2), true
	case 'I':
		return num(hour12(hour), 2), true
	case 'j':
		return num(t.YearDay(), 3), true
	case 'k':
		return spaced(hour, 2), true
	case 'l':
		return spaced(hour12(hour), 2), true
	case 'm':
		return num(int(month), 2), true
	case 'M':
		return num(minute, 2), true
	case 'n':
		return text("\n"), true
	case 'N':
		digits := strconv.Itoa(t.Nanosecond())
		digits = strings.Repeat("0", 9-len(digits)) + digits
		if width > 0 && width < 9 {
			digits = digits[:width]
		}
		return text(digits), true
	case 'p':
		return text(meridian(hour)), true
	case 'P':
		return text(strings.ToLower(meridian(hour))), true
	case 'q':
		return num((int(month)-1)/3+1, 1), true
	case 'r':
		return text(Format(t, "%I:%M:%S %p")), true
	case 'R':
		return text(Format(t, "%H:%M")), true
	case 's':
		return conv{text: strconv.FormatInt(t.Unix(), 10), fill: '0', width: 1}, true
	case 'S':
		return num(sec, 2), true
	case 't':
		return text("\t"), true
	case 'T', 'X':
		return text(Format(t, "%H:%M:%S")), true
	case 'u':
		return num((int(t.Weekday())+6)%7+1, 1), true
	case 'U':
		return num((t.YearDay()+6-int(t.Weekday()))/7, 2), true
	case 'V':
		_, w := t.ISOWeek()
		return num(w, 2), true
	case 'w':
		return num(int(t.Weekday()), 1), true
	case 'W':
		return num((t.YearDay()+6-(int(t.Weekday())+6)%7)/7, 2), true
	case 'y':
		return num(year%100, 2), true
	case 'Y':
		return num(year, 1), true
	case 'z':
		if colons > 3 {
			return conv{}, false
		}
		return text(numericZone(t, colons)), true
	case 'Z':
		name, _ := t.Zone()
		if name == "" {
			name = numericZone(t, 0)
		}
		return text(name), true
	case '+':
		return text(Format(t, Default)), true
	}
	return conv{}, false
}

// field applies flags and width. A numeric conversion pads with its default
// fill unless a flag changes it; text pads with spaces only when widened.
func field(c conv, pad byte, width int, upper, swap bool, letter byte) string {
	s := c.text
	switch {
	case upper:
		s = strings.ToUpper(s)
	case swap && (letter == 'Z' || letter == 'p'):
		s = strings.ToLower(s)
	case swap:
		s = strings.ToUpper(s)
	}
	if letter == 'N' {
		return s
	}
	w, fill := c.width, c.fill
	if width >= 0 {
		w = width
	}
	switch pad {
	case '-':
		if c.fill != 0 {
			s = strings.TrimLeft(s, " ")
		}
		return s
	case '_':
		fill = ' '
	case '0':
		fill = '0'
	}
	if fill == 0 {
		fill = ' '
	}
	if c.fill == 0 && width < 0 {
		return s
	}
	negative := strings.HasPrefix(s, "-")
	if negative && fill == '0' {
		s = s[1:]
		w--
	}
	if len(s) < w {
		s = strings.Repeat(string(fill), w-len(s)) + s
	}
	if negative && fill == '0' {
		s = "-" + s
	}
	return s
}

// numericZone renders %z (+hhmm), %:z (+hh:mm), %::z (+hh:mm:ss) and %:::z
// (the shortest of +hh, +hh:mm and +hh:mm:ss that is exact).
func numericZone(t time.Time, colons int) string {
	_, offset := t.Zone()
	sign := byte('+')
	if offset < 0 {
		sign, offset = '-', -offset
	}
	h, m, s := offset/3600, offset/60%60, offset%60
	two := func(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }
	switch colons {
	case 1:
		return string(sign) + two(h) + ":" + two(m)
	case 2:
		return string(sign) + two(h) + ":" + two(m) + ":" + two(s)
	case 3:
		switch {
		case s != 0:
			return string(sign) + two(h) + ":" + two(m) + ":" + two(s)
		case m != 0:
			return string(sign) + two(h) + ":" + two(m)
		}
		return string(sign) + two(h)
	}
	return string(sign) + two(h) + two(m)
}

func hour12(hour int) int {
	if hour%12 == 0 {
		return 12
	}
	return hour % 12
}

func meridian(hour int) string {
	if hour < 12 {
		return "AM"
	}
	return "PM"
}
