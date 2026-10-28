package datetext

import "time"

// isoDate reads YYYY-MM-DD, with an optional T before a time of day.
func (p *parser) isoDate(year, month token) error {
	day, ok := p.peek(2)
	if !ok || day.kind != tokNumber || !day.signed || day.value >= 0 {
		return ErrInvalid
	}
	p.pos += 3
	if err := p.it.setDate(year.value, year.digits, -month.value, -day.value); err != nil {
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
		return p.it.setDate(first.value, first.digits, second.value, third.value)
	case hasThird:
		return p.it.setDate(third.value, third.digits, first.value, second.value)
	case first.digits >= 3:
		return ErrInvalid
	}
	return p.it.setDate(0, 0, first.value, second.value)
}

// dayMonth reads DD MONTH and DD-MONTH-YYYY.
func (p *parser) dayMonth(day token, month time.Month) error {
	p.pos += 2
	if year, ok := p.peek(0); ok && year.kind == tokNumber && year.signed && year.value < 0 {
		p.pos++
		return p.it.setDate(-year.value, year.digits, int64(month), day.value)
	}
	return p.it.setDate(0, 0, int64(month), day.value)
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
			return p.it.setDate(0, 0, int64(month), -day.value)
		}
		p.pos++
		return p.it.setDate(-year.value, year.digits, int64(month), -day.value)
	}
	return p.it.setDate(0, 0, int64(month), day.value)
}

// pureNumber reads a number standing alone: a year after a yearless date,
// YYYYMMDD, or HH / HHMM.
func (p *parser) pureNumber(t token) error {
	if t.frac != "" {
		return ErrInvalid
	}
	switch {
	case p.it.date.set && p.it.date.yearDigits == 0 && !p.it.rels && (p.it.clock.set || t.digits > 2):
		p.it.date.year, p.it.date.yearDigits = t.value, t.digits
		return nil
	case t.digits > 4:
		return p.it.setDate(t.value/10000, t.digits-4, t.value/100%100, t.value%100)
	case t.digits <= 2:
		return p.it.setClock(t.value, 0, 0, 0)
	}
	return p.it.setClock(t.value/100, t.value%100, 0, 0)
}
