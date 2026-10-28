package datetext

import (
	"errors"
	"strconv"
	"time"
)

// ErrInvalid reports date text the grammar does not read.
var ErrInvalid = errors.New("invalid date")

// relative accumulates relative items: calendar fields apply before the
// fixed seconds, so a day keeps its wall-clock time across a zone change.
type relative struct {
	years, months, days, seconds int64
}

func (r *relative) add(o relative) {
	r.years += o.years
	r.months += o.months
	r.days += o.days
	r.seconds += o.seconds
}

func (r relative) negate() relative {
	return relative{years: -r.years, months: -r.months, days: -r.days, seconds: -r.seconds}
}

// items is what one date string set. Each absolute item may appear once.
type items struct {
	date                bool
	year, month, day    int64
	yearDigits          int
	clock               bool
	hour, minute, sec   int64
	nanos               int64
	meridian            byte // 0, 'a' or 'p'
	zone                bool
	offset              int64
	weekday             bool
	dow                 time.Weekday
	ordinal             int64
	rel                 relative
	rels                bool
	epoch               bool
	epochSec, epochNsec int64
}

type parser struct {
	toks []token
	pos  int
	it   items
	// colonTime is set right after an HH:MM time with no meridian, where a
	// signed number is that time's zone offset rather than a relative count.
	colonTime bool
}

// Parse reads text in the date -d grammar: calendar dates (2026-10-26,
// 10/26/2026, 26 Oct 2026, Oct 26), times of day with an optional meridian
// and zone, weekdays with ordinals, relative items (2 days ago, next month,
// +1 week, yesterday) and @SECONDS. now is the reference instant and loc the
// zone that zoneless text is read in; the result is in loc.
func Parse(text string, now time.Time, loc *time.Location) (time.Time, error) {
	toks, ok := lex(text)
	if !ok {
		return time.Time{}, ErrInvalid
	}
	p := &parser{toks: toks}
	for p.pos < len(p.toks) {
		after := p.colonTime
		p.colonTime = false
		if err := p.item(after); err != nil {
			return time.Time{}, err
		}
	}
	return p.it.resolve(now, loc)
}

func (p *parser) peek(k int) (token, bool) {
	if p.pos+k < len(p.toks) {
		return p.toks[p.pos+k], true
	}
	return token{}, false
}

func (p *parser) peekWord(k int) string {
	t, ok := p.peek(k)
	if !ok || t.kind != tokWord {
		return ""
	}
	return t.text
}

func (p *parser) item(afterTime bool) error {
	t := p.toks[p.pos]
	switch {
	case t.isPunct(","):
		p.pos++
		return nil
	case t.isPunct("@"):
		return p.epoch()
	case t.kind == tokNumber && t.signed:
		return p.signedNumber(t, afterTime)
	case t.kind == tokNumber:
		return p.number(t)
	case t.kind == tokWord:
		return p.word(t)
	}
	return ErrInvalid
}

// epoch reads @SECONDS[.FRACTION], which stands alone.
func (p *parser) epoch() error {
	t, ok := p.peek(1)
	if !ok || t.kind != tokNumber || p.pos != 0 || len(p.toks) != 2 {
		return ErrInvalid
	}
	p.it.epoch, p.it.epochSec, p.it.epochNsec = true, t.value, fraction(t.frac)
	if t.value < 0 {
		p.it.epochNsec = -p.it.epochNsec
	}
	p.pos += 2
	return nil
}

func (p *parser) signedNumber(t token, afterTime bool) error {
	if afterTime && !p.it.zone {
		p.pos++
		return p.offset(t)
	}
	if u, ok := units[p.peekWord(1)]; ok && t.frac == "" {
		p.pos += 2
		p.relative(u.scale(t.value))
		return nil
	}
	return ErrInvalid
}

// offset reads a zone correction: [+-]H, [+-]HHMM or [+-]HH:MM.
func (p *parser) offset(t token) error {
	if p.it.zone || t.frac != "" {
		return ErrInvalid
	}
	sign, hours := int64(1), t.value
	if hours < 0 {
		sign, hours = -1, -hours
	}
	minutes := int64(0)
	if next, ok := p.peek(0); ok && next.isPunct(":") {
		m, ok := p.peek(1)
		if !ok || m.kind != tokNumber || m.signed || m.digits != 2 || t.digits > 2 {
			return ErrInvalid
		}
		minutes, p.pos = m.value, p.pos+2
	} else if t.digits > 2 {
		hours, minutes = hours/100, hours%100
	}
	if hours > 24 || minutes > 59 {
		return ErrInvalid
	}
	p.it.zone, p.it.offset = true, sign*(hours*3600+minutes*60)
	return nil
}

func (p *parser) number(t token) error {
	next, _ := p.peek(1)
	switch {
	case next.isPunct(":"):
		return p.clock(t)
	case next.kind == tokNumber && next.signed && next.value < 0:
		return p.isoDate(t, next)
	case next.isPunct("/"):
		return p.slashDate(t)
	case next.kind == tokWord && lookupMonth(next.text) != 0:
		return p.dayMonth(t, lookupMonth(next.text))
	case next.kind == tokWord && (next.text == "am" || next.text == "pm"):
		if err := p.setClock(t.value, 0, 0, 0); err != nil {
			return err
		}
		p.it.meridian = next.text[0]
		p.pos += 2
		return nil
	}
	if u, ok := units[p.peekWord(1)]; ok {
		if t.frac != "" {
			return ErrInvalid
		}
		p.pos += 2
		p.relative(u.scale(t.value))
		return nil
	}
	p.pos++
	return p.pureNumber(t)
}

// clock reads HH:MM[:SS[.FRACTION]] [am|pm].
func (p *parser) clock(hour token) error {
	minute, ok := p.peek(2)
	if !ok || minute.kind != tokNumber || minute.signed || minute.frac != "" || hour.frac != "" {
		return ErrInvalid
	}
	p.pos += 3
	sec, nanos := int64(0), int64(0)
	if colon, ok := p.peek(0); ok && colon.isPunct(":") {
		s, ok := p.peek(1)
		if !ok || s.kind != tokNumber || s.signed {
			return ErrInvalid
		}
		sec, nanos = s.value, fraction(s.frac)
		p.pos += 2
	}
	if err := p.setClock(hour.value, minute.value, sec, nanos); err != nil {
		return err
	}
	if w := p.peekWord(0); w == "am" || w == "pm" {
		p.it.meridian = w[0]
		p.pos++
		return nil
	}
	p.colonTime = true
	return nil
}

func (p *parser) setClock(hour, minute, sec, nanos int64) error {
	if p.it.clock {
		return ErrInvalid
	}
	p.it.clock, p.it.hour, p.it.minute, p.it.sec, p.it.nanos = true, hour, minute, sec, nanos
	return nil
}

// isoDate reads YYYY-MM-DD, with an optional T before a time of day.
func (p *parser) isoDate(year, month token) error {
	day, ok := p.peek(2)
	if !ok || day.kind != tokNumber || !day.signed || day.value >= 0 {
		return ErrInvalid
	}
	p.pos += 3
	if err := p.setDate(year.value, year.digits, -month.value, -day.value); err != nil {
		return err
	}
	if p.peekWord(0) == "t" {
		if next, ok := p.peek(1); ok && next.kind == tokNumber {
			p.pos++
		}
	}
	return nil
}

// slashDate reads MM/DD, MM/DD/YY[YY] or YYYY/MM/DD.
func (p *parser) slashDate(first token) error {
	second, ok := p.peek(2)
	if !ok || second.kind != tokNumber || second.signed {
		return ErrInvalid
	}
	p.pos += 3
	var third token
	hasThird := false
	if slash, ok := p.peek(0); ok && slash.isPunct("/") {
		third, hasThird = p.peek(1)
		if !hasThird || third.kind != tokNumber || third.signed {
			return ErrInvalid
		}
		p.pos += 2
	}
	switch {
	case first.digits >= 3 && hasThird:
		return p.setDate(first.value, first.digits, second.value, third.value)
	case hasThird:
		return p.setDate(third.value, third.digits, first.value, second.value)
	case first.digits >= 3:
		return ErrInvalid
	}
	return p.setDate(0, 0, first.value, second.value)
}

// dayMonth reads DD MONTH and DD-MONTH-YYYY.
func (p *parser) dayMonth(day token, month time.Month) error {
	p.pos += 2
	if year, ok := p.peek(0); ok && year.kind == tokNumber && year.signed && year.value < 0 {
		p.pos++
		return p.setDate(-year.value, year.digits, int64(month), day.value)
	}
	return p.setDate(0, 0, int64(month), day.value)
}

func (p *parser) setDate(year int64, digits int, month, day int64) error {
	if p.it.date {
		return ErrInvalid
	}
	p.it.date, p.it.year, p.it.yearDigits, p.it.month, p.it.day = true, year, digits, month, day
	return nil
}

// pureNumber reads a number standing alone: a year after a yearless date,
// YYYYMMDD, or HH / HHMM.
func (p *parser) pureNumber(t token) error {
	if t.frac != "" {
		return ErrInvalid
	}
	switch {
	case p.it.date && p.it.yearDigits == 0 && !p.it.rels && (p.it.clock || t.digits > 2):
		p.it.year, p.it.yearDigits = t.value, t.digits
		return nil
	case t.digits > 4:
		return p.setDate(t.value/10000, t.digits-4, t.value/100%100, t.value%100)
	case t.digits <= 2:
		return p.setClock(t.value, 0, 0, 0)
	}
	return p.setClock(t.value/100, t.value%100, 0, 0)
}

func (p *parser) word(t token) error {
	w := t.text
	if m := lookupMonth(w); m != 0 {
		return p.monthDay(m)
	}
	if d, ok := lookupWeekday(w); ok {
		p.pos++
		return p.setWeekday(d, 0)
	}
	if n, ok := ordinals[w]; ok {
		next := p.peekWord(1)
		if u, ok := units[next]; ok {
			p.pos += 2
			p.relative(u.scale(n))
			return nil
		}
		if d, ok := lookupWeekday(next); ok {
			p.pos += 2
			return p.setWeekday(d, n)
		}
		return ErrInvalid
	}
	if u, ok := units[w]; ok {
		p.pos++
		p.relative(u.scale(1))
		return nil
	}
	if offset, ok := zones[w]; ok {
		p.pos++
		return p.zone(int64(offset))
	}
	p.pos++
	switch w {
	case "today", "now":
		p.it.rels = true
	case "yesterday":
		p.relative(relative{days: -1})
	case "tomorrow":
		p.relative(relative{days: 1})
	default:
		return ErrInvalid
	}
	return nil
}

// monthDay reads MONTH DD and MONTH-DD-YYYY; a following year is a pure number.
func (p *parser) monthDay(month time.Month) error {
	day, ok := p.peek(1)
	if !ok || day.kind != tokNumber {
		return ErrInvalid
	}
	p.pos += 2
	if day.signed {
		if day.value >= 0 {
			return ErrInvalid
		}
		year, ok := p.peek(0)
		if !ok || year.kind != tokNumber || !year.signed || year.value >= 0 {
			return p.setDate(0, 0, int64(month), -day.value)
		}
		p.pos++
		return p.setDate(-year.value, year.digits, int64(month), -day.value)
	}
	return p.setDate(0, 0, int64(month), day.value)
}

func (p *parser) setWeekday(d time.Weekday, ordinal int64) error {
	if p.it.weekday {
		return ErrInvalid
	}
	p.it.weekday, p.it.dow, p.it.ordinal = true, d, ordinal
	return nil
}

// zone reads a zone abbreviation, an optional dst, and an optional offset
// from it (utc+2).
func (p *parser) zone(offset int64) error {
	if p.it.zone {
		return ErrInvalid
	}
	if p.peekWord(0) == "dst" {
		offset += 3600
		p.pos++
	}
	if t, ok := p.peek(0); ok && t.kind == tokNumber && t.signed {
		p.pos++
		if err := p.offset(t); err != nil {
			return err
		}
		offset += p.it.offset
	}
	p.it.zone, p.it.offset = true, offset
	return nil
}

// relative records one relative item; a following ago negates only it.
func (p *parser) relative(r relative) {
	if p.peekWord(0) == "ago" {
		r = r.negate()
		p.pos++
	}
	p.it.rels = true
	p.it.rel.add(r)
}

// fraction converts decimal digits after the point to nanoseconds.
func fraction(digits string) int64 {
	if len(digits) > 9 {
		digits = digits[:9]
	}
	for len(digits) < 9 {
		digits += "0"
	}
	n, _ := strconv.ParseInt(digits, 10, 64)
	return n
}

// resolve computes the instant: the absolute items in their zone, then the
// weekday, then calendar relative items, then fixed seconds. The time of day
// is midnight unless the text gives one or has only relative items.
func (it items) resolve(now time.Time, loc *time.Location) (time.Time, error) {
	if it.epoch {
		return time.Unix(it.epochSec, it.epochNsec).In(loc), nil
	}
	zone := loc
	if it.zone {
		zone = time.FixedZone("", int(it.offset))
	}
	base := now.In(zone)
	year, month, day := base.Date()
	hour, minute, sec := base.Clock()
	nanos := base.Nanosecond()
	if it.date {
		if it.yearDigits > 0 {
			year = int(fullYear(it.year, it.yearDigits))
		}
		if it.month < 1 || it.month > 12 || it.day < 1 || it.day > int64(daysIn(time.Month(it.month), year)) {
			return time.Time{}, ErrInvalid
		}
		month, day = time.Month(it.month), int(it.day)
	}
	switch {
	case it.clock:
		h, err := meridianHour(it.hour, it.meridian)
		if err != nil || it.minute > 59 || it.sec > 60 {
			return time.Time{}, ErrInvalid
		}
		hour, minute, sec, nanos = int(h), int(it.minute), int(it.sec), int(it.nanos)
	case !it.rels || it.date || it.weekday:
		hour, minute, sec, nanos = 0, 0, 0, 0
	}
	if it.weekday && !it.date {
		wd := time.Date(year, month, day, 12, 0, 0, 0, zone).Weekday()
		shift := (int(it.dow) - int(wd) + 7) % 7
		ordinal := int(it.ordinal)
		if ordinal > 0 && wd != it.dow {
			ordinal--
		}
		day += shift + 7*ordinal
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
