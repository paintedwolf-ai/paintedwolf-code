package datetext

import (
	"strconv"
	"strings"
	"time"
)

// Zone is the location a program's TZ value names. Unset or empty is UTC.
// A zone database name (America/New_York, with or without a leading colon)
// loads from the host database; a POSIX STDoffset value (UTC0, EST5,
// <+0530>-5:30) is a fixed zone whose offset is west of UTC. Daylight rules
// after the standard offset are not applied. Anything else is UTC under the
// name given, as the C library treats an unknown TZ.
func Zone(environment []string) *time.Location {
	tz, ok := "", false
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, "TZ="); found {
			tz, ok = value, true
		}
	}
	if !ok || tz == "" {
		return time.UTC
	}
	name := strings.TrimPrefix(tz, ":")
	if name == "UTC" || name == "UTC0" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	if loc, ok := posixZone(name); ok {
		return loc
	}
	abbreviation := strings.TrimRight(name, "+-0123456789:")
	if abbreviation == "" {
		abbreviation = "UTC"
	}
	return time.FixedZone(abbreviation, 0)
}

// posixZone reads STD[+-]hh[:mm[:ss]], where STD is three or more letters
// or a <quoted> name.
func posixZone(tz string) (*time.Location, bool) {
	var name string
	rest := tz
	if strings.HasPrefix(rest, "<") {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return nil, false
		}
		name, rest = rest[1:end], rest[end+1:]
	} else {
		i := 0
		for i < len(rest) && isLetter(lowerByte(rest[i])) {
			i++
		}
		name, rest = rest[:i], rest[i:]
		if len(name) < 3 {
			return nil, false
		}
	}
	sign := 1
	switch {
	case strings.HasPrefix(rest, "+"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "-"):
		sign, rest = -1, rest[1:]
	}
	end := 0
	for end < len(rest) && (isDigit(rest[end]) || rest[end] == ':') {
		end++
	}
	if end == 0 {
		return nil, false
	}
	parts := strings.Split(rest[:end], ":")
	seconds := 0
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || i > 2 {
			return nil, false
		}
		seconds += n * []int{3600, 60, 1}[i]
	}
	if seconds > 24*3600 {
		return nil, false
	}
	return time.FixedZone(name, -sign*seconds), true
}
