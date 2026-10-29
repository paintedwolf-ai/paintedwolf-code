package mutation

import (
	"strconv"
	"strings"
	"time"
)

// parseStamp reads touch -t [[CC]YY]MMDDhhmm[.ss] in local time. A two-digit
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
	t := time.Date(year, time.Month(month), day, hour, minute, min(second, 59), 0, time.Local)
	if t.Month() != time.Month(month) || t.Day() != day || t.Hour() != hour || t.Minute() != minute {
		return time.Time{}, false
	}
	if second == 60 {
		t = t.Add(time.Second)
	}
	return t, true
}

func digits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// dateLayouts are the absolute touch -d forms: ISO 8601 dates and times with
// an optional zone, and RFC 5322 dates. Zoneless forms are local time.
var dateLayouts = []string{
	"2006-01-02",
	"2006-01-02 15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05 Z07:00",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04 MST",
	time.RFC1123,
	time.RFC1123Z,
	"Mon Jan _2 15:04:05 MST 2006",
	"Mon Jan _2 15:04:05 2006",
}

var dateUnits = map[string]time.Duration{
	"second": time.Second, "sec": time.Second, "minute": time.Minute, "min": time.Minute,
	"hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour,
}

// parseDate reads touch -d: now, today, yesterday, tomorrow, @SECONDS, the
// absolute layouts above, and "N UNIT[s] [ago]" relative to now.
func parseDate(value string, now time.Time) (time.Time, bool) {
	text := strings.TrimSpace(value)
	switch strings.ToLower(text) {
	case "now", "today":
		return now, true
	case "yesterday":
		return now.AddDate(0, 0, -1), true
	case "tomorrow":
		return now.AddDate(0, 0, 1), true
	}
	if epoch, ok := strings.CutPrefix(text, "@"); ok {
		whole, frac, _ := strings.Cut(epoch, ".")
		seconds, err := strconv.ParseInt(whole, 10, 64)
		if err != nil || (frac != "" && !digits(frac)) || len(frac) > 9 {
			return time.Time{}, false
		}
		nanos := int64(0)
		if frac != "" {
			nanos, _ = strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
			if strings.HasPrefix(whole, "-") {
				nanos = -nanos
			}
		}
		return time.Unix(seconds, nanos), true
	}
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return t, true
		}
	}
	fields := strings.Fields(strings.ToLower(text))
	if len(fields) == 2 || (len(fields) == 3 && fields[2] == "ago") {
		n, err := strconv.Atoi(fields[0])
		unit, ok := dateUnits[strings.TrimSuffix(fields[1], "s")]
		if err == nil && ok {
			offset := time.Duration(n) * unit
			if len(fields) == 3 {
				offset = -offset
			}
			return now.Add(offset), true
		}
	}
	return time.Time{}, false
}
