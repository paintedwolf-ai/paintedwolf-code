package mutation

import (
	"strconv"
	"strings"
	"time"
)

// parseStamp reads touch -t [[CC]YY]MMDDhhmm[.ss] in now's zone. A two-digit
// year 69-99 is 19YY and 00-68 is 20YY, as POSIX specifies.
func parseStamp(value string, now time.Time) (time.Time, bool) {
	main, secs, hasSecs := strings.Cut(value, ".")
	if !digits(main) || (hasSecs && (len(secs) != 2 || !digits(secs))) {
		return time.Time{}, false
	}
	year := now.Year()
	switch len(main) {
	case 8:
	case 10:
		yy, _ := strconv.Atoi(main[:2])
		year = 2000 + yy
		if yy >= 69 {
			year = 1900 + yy
		}
		main = main[2:]
	case 12:
		year, _ = strconv.Atoi(main[:4])
		main = main[4:]
	default:
		return time.Time{}, false
	}
	field := func(i int) int { n, _ := strconv.Atoi(main[i : i+2]); return n }
	month, day, hour, minute, second := field(0), field(2), field(4), field(6), 0
	if hasSecs {
		second, _ = strconv.Atoi(secs)
	}
	if second > 60 {
		return time.Time{}, false
	}
	// POSIX admits a leap second; it lands on the next minute's first second.
	t := time.Date(year, time.Month(month), day, hour, minute, min(second, 59), 0, now.Location())
	if t.Month() != time.Month(month) || t.Day() != day || t.Hour() != hour || t.Minute() != minute {
		return time.Time{}, false
	}
	if second == 60 {
		t = t.Add(time.Second)
	}
	return t, true
}

func digits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
