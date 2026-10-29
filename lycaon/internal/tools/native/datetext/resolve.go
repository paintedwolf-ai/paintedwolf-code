package datetext

import "time"

// items is what one date string set. Each absolute item may appear once.
type items struct {
	date    calendarDate
	clock   clockTime
	weekday weekdayItem
	// zone is set with offset, seconds east of UTC, when the text names one.
	zone   bool
	offset int64
	rel    relative
	rels   bool
	// epoch is @SECONDS, which stands alone.
	epoch *time.Time
}

// calendarDate is a calendar date item; yearDigits is 0 when no year was given.
type calendarDate struct {
	set              bool
	year, month, day int64
	yearDigits       int
}

// clockTime is a time-of-day item; meridian is 0, 'a' or 'p'.
type clockTime struct {
	set                      bool
	hour, minute, sec, nanos int64
	meridian                 byte
}

// weekdayItem is a day-of-week item with its ordinal (next is 1, last -1).
type weekdayItem struct {
	set     bool
	day     time.Weekday
	ordinal int64
}

func (it *items) setDate(year int64, digits int, month, day int64) error {
	if it.date.set {
		return ErrInvalid
	}
	it.date = calendarDate{set: true, year: year, month: month, day: day, yearDigits: digits}
	return nil
}

func (it *items) setClock(hour, minute, sec, nanos int64) error {
	if it.clock.set {
		return ErrInvalid
	}
	it.clock = clockTime{set: true, hour: hour, minute: minute, sec: sec, nanos: nanos}
	return nil
}

func (it *items) setWeekday(d time.Weekday, ordinal int64) error {
	if it.weekday.set {
		return ErrInvalid
	}
	it.weekday = weekdayItem{set: true, day: d, ordinal: ordinal}
	return nil
}

// resolve computes the instant: the absolute items in their zone, then the
// weekday, then calendar relative items, then fixed seconds. The time of day
// is midnight unless the text gives one or has only relative items.
func (it items) resolve(now time.Time, loc *time.Location) (time.Time, error) {
	if it.epoch != nil {
		return it.epoch.In(loc), nil
	}
	zone := loc
	if it.zone {
		zone = time.FixedZone("", int(it.offset))
	}
	base := now.In(zone)
	year, month, day := base.Date()
	hour, minute, sec := base.Clock()
	nanos := base.Nanosecond()
	if d := it.date; d.set {
		if d.yearDigits > 0 {
			year = int(fullYear(d.year, d.yearDigits))
		}
		if d.month < 1 || d.month > 12 || d.day < 1 || d.day > int64(daysIn(time.Month(d.month), year)) {
			return time.Time{}, ErrInvalid
		}
		month, day = time.Month(d.month), int(d.day)
	}
	switch {
	case it.clock.set:
		c := it.clock
		h, err := meridianHour(c.hour, c.meridian)
		if err != nil || c.minute > 59 || c.sec > 60 {
			return time.Time{}, ErrInvalid
		}
		hour, minute, sec, nanos = int(h), int(c.minute), int(c.sec), int(c.nanos)
	case !it.rels || it.date.set || it.weekday.set:
		hour, minute, sec, nanos = 0, 0, 0, 0
	}
	if w := it.weekday; w.set && !it.date.set {
		day += weekdayShift(time.Date(year, month, day, 12, 0, 0, 0, zone).Weekday(), w)
	}
	t := time.Date(year+int(it.rel.years), month+time.Month(it.rel.months), day+int(it.rel.days), hour, minute, sec, nanos, zone)
	return t.Add(time.Duration(it.rel.seconds) * time.Second).In(loc), nil
}

// fullYear reads a two-digit year as 1969-2068.
func fullYear(year int64, digits int) int64 {
	if digits > 2 {
		return year
	}
	if year >= 69 {
		return 1900 + year
	}
	return 2000 + year
}

func meridianHour(hour int64, meridian byte) (int64, error) {
	switch {
	case meridian == 0 && hour <= 23:
		return hour, nil
	case meridian == 0 || hour < 1 || hour > 12:
		return 0, ErrInvalid
	case meridian == 'a':
		return hour % 12, nil
	}
	return hour%12 + 12, nil
}

func daysIn(month time.Month, year int) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// weekdayShift is the days from today to the named weekday. A bare or this
// weekday is its next occurrence, today included; next N skips today's and
// counts N occurrences ahead; last N counts back N weeks from the next
// occurrence, so last on that weekday is a week ago.
func weekdayShift(today time.Weekday, w weekdayItem) int {
	shift := (int(w.day) - int(today) + 7) % 7
	switch {
	case w.ordinal > 0 && shift == 0:
		shift += 7 * int(w.ordinal)
	case w.ordinal > 0:
		shift += 7 * int(w.ordinal-1)
	case w.ordinal < 0:
		shift += 7 * int(w.ordinal)
	}
	return shift
}
