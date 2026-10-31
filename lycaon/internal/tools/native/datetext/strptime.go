package datetext

import (
	"strings"
	"time"
)

// ParseFormat reads text against a strptime FORMAT, as date -j -f does.
// Fields the format does not set keep base's values, and the result is
// normalized as calendar arithmetic does (February 30 is March 2). It
// returns the text left after the format matched.
func ParseFormat(format, text string, base time.Time) (time.Time, string, error) {
	s := &scan{text: text, loc: base.Location()}
	s.year, s.month, s.day = base.Date()
	s.hour, s.minute, s.sec = base.Clock()
	if err := s.format(format); err != nil {
		return time.Time{}, "", err
	}
	if s.epoch != nil {
		return s.epoch.In(base.Location()), s.text, nil
	}
	if s.meridian != 0 {
		s.hour %= 12
		if s.meridian == 'p' {
			s.hour += 12
		}
	}
	zone := s.loc
	if s.offset != nil {
		zone = time.FixedZone("", *s.offset)
	}
	return time.Date(s.year, s.month, s.day, s.hour, s.minute, s.sec, 0, zone).In(base.Location()), s.text, nil
}

// scan is one strptime run: the remaining text and the fields set so far.
type scan struct {
	text                         string
	loc                          *time.Location
	year, day, hour, minute, sec int
	month                        time.Month
	meridian                     byte
	offset                       *int
	epoch                        *time.Time
}

func (s *scan) format(format string) error {
	for i := 0; i < len(format); i++ {
		c := format[i]
		switch {
		case isSpace(c):
			s.text = strings.TrimLeft(s.text, " \t\n\r\v\f")
			continue
		case c != '%':
			if s.text == "" || s.text[0] != c {
				return ErrInvalid
			}
			s.text = s.text[1:]
			continue
		}
		i++
		for i < len(format) && (format[i] == 'E' || format[i] == 'O') {
			i++
		}
		if i >= len(format) {
			return ErrInvalid
		}
		if err := s.conversion(format[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *scan) conversion(c byte) error {
	var err error
	switch c {
	case '%':
		if !strings.HasPrefix(s.text, "%") {
			return ErrInvalid
		}
		s.text = s.text[1:]
	case 'n', 't':
		s.text = strings.TrimLeft(s.text, " \t\n\r\v\f")
	case 'Y':
		s.year, err = s.number(4, 0, 9999)
	case 'C':
		var century int
		century, err = s.number(2, 0, 99)
		s.year = century*100 + s.year%100
	case 'y':
		var y int
		y, err = s.number(2, 0, 99)
		s.year = int(fullYear(int64(y), 2))
	case 'm':
		var m int
		m, err = s.number(2, 1, 12)
		s.month = time.Month(m)
	case 'd', 'e':
		s.day, err = s.number(2, 1, 31)
	case 'j':
		// A day of the year is read and checked but sets no field, so the
		// month and day come from the base unless the format sets them.
		_, err = s.number(3, 1, 366)
	case 'H', 'k':
		s.hour, err = s.number(2, 0, 23)
	case 'I', 'l':
		s.hour, err = s.number(2, 1, 12)
	case 'M':
		s.minute, err = s.number(2, 0, 59)
	case 'S':
		s.sec, err = s.number(2, 0, 60)
	case 'p':
		err = s.meridianWord()
	case 'b', 'B', 'h':
		err = s.monthName()
	case 'a', 'A':
		err = s.weekdayName()
	case 's':
		err = s.seconds()
	case 'z':
		err = s.zoneOffset()
	case 'Z':
		err = s.zoneName()
	case 'T':
		err = s.format("%H:%M:%S")
	case 'R':
		err = s.format("%H:%M")
	case 'r':
		err = s.format("%I:%M:%S %p")
	case 'D', 'x':
		err = s.format("%m/%d/%y")
	case 'F':
		err = s.format("%Y-%m-%d")
	case 'c':
		err = s.format("%a %b %e %H:%M:%S %Y")
	case 'X':
		err = s.format("%H:%M:%S")
	default:
		return ErrInvalid
	}
	return err
}

// number reads up to width digits after optional spaces, within [lo, hi].
func (s *scan) number(width, lo, hi int) (int, error) {
	s.text = strings.TrimLeft(s.text, " ")
	n, i := 0, 0
	for ; i < len(s.text) && i < width && isDigit(s.text[i]); i++ {
		n = n*10 + int(s.text[i]-'0')
	}
	if i == 0 || n < lo || n > hi {
		return 0, ErrInvalid
	}
	s.text = s.text[i:]
	return n, nil
}

func (s *scan) word() string {
	s.text = strings.TrimLeft(s.text, " ")
	i := 0
	for i < len(s.text) && isLetter(lowerByte(s.text[i])) {
		i++
	}
	return strings.ToLower(s.text[:i])
}

func (s *scan) meridianWord() error {
	w := s.word()
	if w != "am" && w != "pm" {
		return ErrInvalid
	}
	s.meridian = w[0]
	s.text = strings.TrimLeft(s.text, " ")[2:]
	return nil
}

func (s *scan) monthName() error {
	w := s.word()
	m := lookupMonth(w)
	if m == 0 {
		return ErrInvalid
	}
	s.month = m
	s.text = strings.TrimLeft(s.text, " ")[len(w):]
	return nil
}

func (s *scan) weekdayName() error {
	w := s.word()
	if _, ok := lookupWeekday(w); !ok {
		return ErrInvalid
	}
	s.text = strings.TrimLeft(s.text, " ")[len(w):]
	return nil
}

func (s *scan) seconds() error {
	s.text = strings.TrimLeft(s.text, " ")
	sign := int64(1)
	if strings.HasPrefix(s.text, "-") {
		sign, s.text = -1, s.text[1:]
	}
	i := 0
	var n int64
	for ; i < len(s.text) && isDigit(s.text[i]) && i < 18; i++ {
		n = n*10 + int64(s.text[i]-'0')
	}
	if i == 0 {
		return ErrInvalid
	}
	s.text = s.text[i:]
	t := time.Unix(sign*n, 0)
	s.epoch = &t
	return nil
}

// zoneOffset reads Z, +hh, +hhmm or +hh:mm.
func (s *scan) zoneOffset() error {
	s.text = strings.TrimLeft(s.text, " ")
	if strings.HasPrefix(s.text, "Z") || strings.HasPrefix(s.text, "z") {
		zero := 0
		s.offset, s.text = &zero, s.text[1:]
		return nil
	}
	if s.text == "" || (s.text[0] != '+' && s.text[0] != '-') {
		return ErrInvalid
	}
	sign := 1
	if s.text[0] == '-' {
		sign = -1
	}
	s.text = s.text[1:]
	hours, err := s.number(2, 0, 24)
	if err != nil {
		return err
	}
	minutes := 0
	s.text = strings.TrimPrefix(s.text, ":")
	if len(s.text) >= 2 && isDigit(s.text[0]) {
		if minutes, err = s.number(2, 0, 59); err != nil {
			return err
		}
	}
	offset := sign * (hours*3600 + minutes*60)
	s.offset = &offset
	return nil
}

// zoneName reads UTC, GMT, Z or the name of the base zone.
func (s *scan) zoneName() error {
	s.text = strings.TrimLeft(s.text, " ")
	i := 0
	for i < len(s.text) && (isLetter(lowerByte(s.text[i]))) {
		i++
	}
	name := s.text[:i]
	switch strings.ToUpper(name) {
	case "UTC", "GMT", "Z", "UT":
		zero := 0
		s.offset = &zero
	default:
		if local, _ := time.Date(s.year, s.month, s.day, s.hour, 0, 0, 0, s.loc).Zone(); name == "" || name != local {
			return ErrInvalid
		}
	}
	s.text = s.text[i:]
	return nil
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
