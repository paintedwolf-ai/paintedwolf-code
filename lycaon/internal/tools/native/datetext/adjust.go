package datetext

import (
	"strconv"
	"strings"
	"time"
)

// Adjust applies one date -v adjustment: [+|-]N followed by y, m, w, d, H, M
// or S, or [+|-] followed by a weekday or month name. A sign moves the field
// by N (a weekday or month name moves forward or back to the next one, and
// stays when already there); no sign sets it. Moving by months keeps the day
// within the target month.
func Adjust(t time.Time, spec string) (time.Time, error) {
	sign := 0
	rest := spec
	switch {
	case strings.HasPrefix(rest, "+"):
		sign, rest = 1, rest[1:]
	case strings.HasPrefix(rest, "-"):
		sign, rest = -1, rest[1:]
	}
	if rest == "" {
		return time.Time{}, ErrInvalid
	}
	if isLetter(lowerByte(rest[0])) {
		return adjustName(t, sign, strings.ToLower(rest))
	}
	digits := strings.TrimRight(rest, "ymwdHMS")
	if len(rest)-len(digits) != 1 || digits == "" {
		return time.Time{}, ErrInvalid
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	unit := rest[len(rest)-1]
	if sign == 0 {
		return setField(t, unit, n)
	}
	return moveField(t, unit, sign*n), nil
}

func moveField(t time.Time, unit byte, n int) time.Time {
	y, m, d := t.Date()
	h, mi, s := t.Clock()
	ns, loc := t.Nanosecond(), t.Location()
	switch unit {
	case 'y':
		return time.Date(y+n, m, d, h, mi, s, ns, loc)
	case 'm':
		first := time.Date(y, m+time.Month(n), 1, h, mi, s, ns, loc)
		return time.Date(first.Year(), first.Month(), min(d, daysIn(first.Month(), first.Year())), h, mi, s, ns, loc)
	case 'w':
		return time.Date(y, m, d+7*n, h, mi, s, ns, loc)
	case 'd':
		return time.Date(y, m, d+n, h, mi, s, ns, loc)
	case 'H':
		return time.Date(y, m, d, h+n, mi, s, ns, loc)
	case 'M':
		return time.Date(y, m, d, h, mi+n, s, ns, loc)
	}
	return time.Date(y, m, d, h, mi, s+n, ns, loc)
}

// setField sets one field; a year below 69 is 20YY and below 100 is 19YY.
func setField(t time.Time, unit byte, n int) (time.Time, error) {
	y, m, d := t.Date()
	h, mi, s := t.Clock()
	ns, loc := t.Nanosecond(), t.Location()
	switch unit {
	case 'y':
		if n < 100 {
			n = int(fullYear(int64(n), 2))
		}
		y = n
	case 'm':
		if n < 1 || n > 12 {
			return time.Time{}, ErrInvalid
		}
		m = time.Month(n)
	case 'd':
		if n < 1 || n > 31 {
			return time.Time{}, ErrInvalid
		}
		d = n
	case 'H':
		if n > 23 {
			return time.Time{}, ErrInvalid
		}
		h = n
	case 'M':
		if n > 59 {
			return time.Time{}, ErrInvalid
		}
		mi = n
	case 'S':
		if n > 60 {
			return time.Time{}, ErrInvalid
		}
		s = n
	default:
		return time.Time{}, ErrInvalid
	}
	return time.Date(y, m, d, h, mi, s, ns, loc), nil
}

// adjustName moves to a named weekday or month. Without a sign a weekday is
// that day of the current Sunday-first week and a month is set in place.
func adjustName(t time.Time, sign int, name string) (time.Time, error) {
	y, m, d := t.Date()
	h, mi, s := t.Clock()
	ns, loc := t.Nanosecond(), t.Location()
	if wd, ok := lookupWeekday(name); ok {
		shift := int(wd) - int(t.Weekday())
		switch {
		case sign > 0:
			shift = (shift + 7) % 7
		case sign < 0:
			shift = -((-shift + 7) % 7)
		}
		return time.Date(y, m, d+shift, h, mi, s, ns, loc), nil
	}
	month := lookupMonth(name)
	if month == 0 {
		return time.Time{}, ErrInvalid
	}
	shift := int(month) - int(m)
	switch {
	case sign > 0:
		shift = (shift + 12) % 12
	case sign < 0:
		shift = -((-shift + 12) % 12)
	}
	return moveField(t, 'm', shift), nil
}
