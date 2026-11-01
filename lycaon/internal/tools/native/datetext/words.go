package datetext

import (
	"strings"
	"time"
)

// lookupMonth reads a month name or its three-letter abbreviation, plus
// sept; it returns 0 for other words.
func lookupMonth(word string) time.Month {
	for m := time.January; m <= time.December; m++ {
		name := strings.ToLower(m.String())
		if word == name || word == name[:3] {
			return m
		}
	}
	if word == "sept" {
		return time.September
	}
	return 0
}

// lookupWeekday reads a weekday name, its three-letter abbreviation, or the
// tues, wednes, thur and thurs spellings.
func lookupWeekday(word string) (time.Weekday, bool) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if word == name || word == name[:3] {
			return d, true
		}
	}
	switch word {
	case "tues":
		return time.Tuesday, true
	case "wednes":
		return time.Wednesday, true
	case "thur", "thurs":
		return time.Thursday, true
	}
	return 0, false
}

// unit is one relative quantity: calendar fields or a fixed duration.
type unit struct {
	years, months, days int
	seconds             int64
}

func (u unit) scale(n int64) relative {
	return relative{years: int64(u.years) * n, months: int64(u.months) * n, days: int64(u.days) * n, seconds: u.seconds * n}
}

var units = map[string]unit{
	"year": {years: 1}, "years": {years: 1},
	"month": {months: 1}, "months": {months: 1},
	"fortnight": {days: 14}, "fortnights": {days: 14},
	"week": {days: 7}, "weeks": {days: 7},
	"day": {days: 1}, "days": {days: 1},
	"hour": {seconds: 3600}, "hours": {seconds: 3600},
	"minute": {seconds: 60}, "minutes": {seconds: 60}, "min": {seconds: 60}, "mins": {seconds: 60},
	"second": {seconds: 1}, "seconds": {seconds: 1}, "sec": {seconds: 1}, "secs": {seconds: 1},
}

// ordinals are the counting words. second is a unit, so it is not one.
var ordinals = map[string]int64{
	"last": -1, "this": 0, "next": 1, "first": 1, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6,
	"seventh": 7, "eighth": 8, "ninth": 9, "tenth": 10, "eleventh": 11, "twelfth": 12,
}

// zones are the abbreviations the grammar knows, as seconds east of UTC.
var zones = map[string]int{
	"gmt": 0, "ut": 0, "utc": 0, "z": 0, "wet": 0, "west": 3600, "bst": 3600,
	"art": -3 * 3600, "brt": -3 * 3600, "brst": -2 * 3600, "nst": -(3*3600 + 1800), "ndt": -(2*3600 + 1800),
	"ast": -4 * 3600, "adt": -3 * 3600, "est": -5 * 3600, "edt": -4 * 3600, "cst": -6 * 3600, "cdt": -5 * 3600,
	"mst": -7 * 3600, "mdt": -6 * 3600, "pst": -8 * 3600, "pdt": -7 * 3600, "akst": -9 * 3600, "akdt": -8 * 3600,
	"hst": -10 * 3600, "hast": -10 * 3600, "hadt": -9 * 3600, "sst": -11 * 3600,
	"wat": 3600, "cet": 3600, "cest": 2 * 3600, "met": 3600, "mez": 3600, "mesz": 2 * 3600,
	"eet": 2 * 3600, "eest": 3 * 3600, "cat": 2 * 3600, "sast": 2 * 3600, "eat": 3 * 3600,
	"msk": 3 * 3600, "msd": 4 * 3600, "ist": 5*3600 + 1800, "sgt": 8 * 3600, "kst": 9 * 3600,
	"jst": 9 * 3600, "gst": 10 * 3600, "nzst": 12 * 3600, "nzdt": 13 * 3600,
}
