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
	nanos := fraction(t.frac)
	if t.value < 0 {
		nanos = -nanos
	}
	at := time.Unix(t.value, nanos)
	p.it.epoch = &at
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
		if err := p.it.setClock(t.value, 0, 0, 0); err != nil {
			return err
		}
		p.it.clock.meridian = next.text[0]
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
	if err := p.it.setClock(hour.value, minute.value, sec, nanos); err != nil {
		return err
	}
	if w := p.peekWord(0); w == "am" || w == "pm" {
		p.it.clock.meridian = w[0]
		p.pos++
		return nil
	}
	p.colonTime = true
	return nil
}

func (p *parser) word(t token) error {
	w := t.text
	if m := lookupMonth(w); m != 0 {
		return p.monthDay(m)
	}
	if d, ok := lookupWeekday(w); ok {
		p.pos++
		return p.it.setWeekday(d, 0)
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
			return p.it.setWeekday(d, n)
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
