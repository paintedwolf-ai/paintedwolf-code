package watch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

const (
	timerAnchorCursorKey = "timer:anchor"
	watchTimerPrefix     = "watch-timer:"
)

// NextTimerOccurrences returns up to count strictly-after UTC firing instants for a timer condition.
func NextTimerOccurrences(cond awaitstore.Condition, anchor, after time.Time, count int) ([]time.Time, error) {
	if cond.Kind != "timer" || count <= 0 {
		return nil, nil
	}
	anchor = anchor.UTC()
	after = after.UTC()
	if cond.DelayMS > 0 {
		return nextIntervalOccurrences(anchor, after, time.Duration(cond.DelayMS)*time.Millisecond, count), nil
	}
	if cond.EveryMinutes > 0 {
		return nextIntervalOccurrences(anchor, after, time.Duration(cond.EveryMinutes)*time.Minute, count), nil
	}
	loc, err := loadConditionLocation(cond.TimeZone)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cond.At) != "" {
		atTime, err := resolveTimerAtInstant(cond.At, loc)
		if err != nil {
			return nil, err
		}
		if atTime.After(after) {
			return []time.Time{atTime}, nil
		}
		return nil, nil
	}
	if cond.Calendar != nil {
		return nextCalendarOccurrences(*cond.Calendar, loc, after, count)
	}
	return nil, nil
}

func nextIntervalOccurrences(anchor, after time.Time, step time.Duration, count int) []time.Time {
	if step <= 0 || count <= 0 {
		return nil
	}
	var firstK int64 = 1
	if !after.Before(anchor) {
		elapsed := after.Sub(anchor)
		firstK = int64(elapsed/step) + 1
	}
	out := make([]time.Time, 0, count)
	for k := firstK; len(out) < count; k++ {
		cand := anchor.Add(time.Duration(k) * step).UTC()
		if cand.After(after) {
			out = append(out, cand)
		}
	}
	return out
}

func loadConditionLocation(tz string) (*time.Location, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" || strings.EqualFold(tz, "Local") {
		return time.UTC, nil
	}
	return time.LoadLocation(tz)
}

func resolveTimerAtInstant(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), nil
	}
	if len(raw) == len("2006-01-02T15:04") && raw[10] == 'T' {
		y, errY := strconv.Atoi(raw[0:4])
		m, errM := strconv.Atoi(raw[5:7])
		d, errD := strconv.Atoi(raw[8:10])
		hh, errH := strconv.Atoi(raw[11:13])
		mm, errMin := strconv.Atoi(raw[14:16])
		if errY == nil && errM == nil && errD == nil && errH == nil && errMin == nil {
			return ResolveLocalWallMinute(loc, y, time.Month(m), d, hh, mm), nil
		}
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04", raw, loc)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// ResolveLocalWallMinute maps a calendar date and HH:MM in loc to UTC:
// repeated fall-back minutes fire once at their first instance; skipped
// spring-forward minutes fire at the first valid minute after the gap.
func ResolveLocalWallMinute(loc *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	targetMin := hour*60 + minute
	noonUTC := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
	start := noonUTC.Add(-26 * time.Hour)
	end := noonUTC.Add(28 * time.Hour)

	seenDay := false
	for u := start; !u.After(end); u = u.Add(time.Minute) {
		local := u.In(loc)
		ly, lm, ld := local.Date()
		sameDay := ly == year && lm == month && ld == day
		if sameDay {
			seenDay = true
			curMin := local.Hour()*60 + local.Minute()
			if curMin >= targetMin {
				return u.UTC()
			}
			continue
		}
		if seenDay {
			// A gap crossing local midnight settles at the first valid minute of the next date.
			return u.UTC()
		}
	}
	return time.Date(year, month, day, hour, minute, 0, 0, loc).UTC()
}

func nextCalendarOccurrences(spec awaitstore.CalendarSpec, loc *time.Location, after time.Time, count int) ([]time.Time, error) {
	if len(spec.Days) == 0 || len(spec.Times) == 0 || count <= 0 {
		return nil, nil
	}
	type hm struct{ h, m int }
	parsedTimes := make([]hm, 0, len(spec.Times))
	for _, raw := range spec.Times {
		raw = strings.TrimSpace(raw)
		if len(raw) != 5 || raw[2] != ':' {
			return nil, fmt.Errorf("invalid calendar time %q", raw)
		}
		hh, errH := strconv.Atoi(raw[:2])
		mm, errM := strconv.Atoi(raw[3:])
		if errH != nil || errM != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return nil, fmt.Errorf("invalid calendar time %q", raw)
		}
		parsedTimes = append(parsedTimes, hm{h: hh, m: mm})
	}
	sort.Slice(parsedTimes, func(i, j int) bool {
		if parsedTimes[i].h != parsedTimes[j].h {
			return parsedTimes[i].h < parsedTimes[j].h
		}
		return parsedTimes[i].m < parsedTimes[j].m
	})

	startLocal := after.In(loc)
	startDate := time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)

	out := make([]time.Time, 0, count)
	seen := map[int64]bool{}
	maxDays := max(400, count*14)
	for d := 0; d < maxDays && len(out) < count; d++ {
		dayUTC := startDate.AddDate(0, 0, d)
		if !calendarMatchesWeekday(spec.Days, dayUTC.Weekday()) {
			continue
		}
		var dayCands []time.Time
		for _, tm := range parsedTimes {
			inst := ResolveLocalWallMinute(loc, dayUTC.Year(), dayUTC.Month(), dayUTC.Day(), tm.h, tm.m)
			if inst.After(after) && !seen[inst.Unix()] {
				seen[inst.Unix()] = true
				dayCands = append(dayCands, inst)
			}
		}
		sort.Slice(dayCands, func(i, j int) bool { return dayCands[i].Before(dayCands[j]) })
		for _, c := range dayCands {
			out = append(out, c)
			if len(out) == count {
				break
			}
		}
	}
	return out, nil
}

func calendarMatchesWeekday(days []string, wd time.Weekday) bool {
	name := strings.ToLower(wd.String())
	for _, entry := range days {
		switch strings.ToLower(strings.TrimSpace(entry)) {
		case "every_day":
			return true
		case "weekdays":
			return wd >= time.Monday && wd <= time.Friday
		case name:
			return true
		}
	}
	return false
}

func timerCursorKey(altIdx int) string {
	return fmt.Sprintf("timer:last:%d", altIdx)
}

func watchTimerAnchor(reg Registration, fallback time.Time) time.Time {
	if raw := strings.TrimSpace(reg.Cursors[timerAnchorCursorKey]); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UTC()
		}
	}
	return fallback.UTC()
}

func (s *Store) scheduleWatchTimer(reg Registration) {
	if s == nil || reg.Lifecycle.State != StateLive {
		return
	}
	anchor := watchTimerAnchor(reg, s.clock.Now())
	var earliest time.Time
	for i, cond := range reg.Conditions {
		if cond.Kind != "timer" {
			continue
		}
		after := anchor
		if raw := strings.TrimSpace(reg.Cursors[timerCursorKey(i)]); raw != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil && parsed.After(after) {
				after = parsed.UTC()
			}
		}
		next, err := NextTimerOccurrences(cond, anchor, after, 1)
		if err != nil || len(next) == 0 {
			continue
		}
		// Expiry wins when a tick falls at or after ExpiresAt.
		if !next[0].Before(reg.Lifecycle.ExpiresAt) {
			continue
		}
		if earliest.IsZero() || next[0].Before(earliest) {
			earliest = next[0]
		}
	}
	if earliest.IsZero() {
		s.heap.Remove(KindTimerTick, watchTimerPrefix+reg.WatchID)
		return
	}
	s.heap.Upsert(KindTimerTick, watchTimerPrefix+reg.WatchID, earliest)
}

func fireWatchTimer(ctx context.Context, s *Store, watchID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	reg, err := loadRegistrationByIDTx(ctx, tx, watchID)
	_ = tx.Rollback()
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if reg.Lifecycle.State != StateLive {
		return nil
	}
	if !reg.Lifecycle.ExpiresAt.After(now) {
		return expireWatch(ctx, s, watchID, now)
	}

	anchor := watchTimerAnchor(reg, now)
	for i, cond := range reg.Conditions {
		if cond.Kind != "timer" {
			continue
		}
		after := anchor
		if raw := strings.TrimSpace(reg.Cursors[timerCursorKey(i)]); raw != "" {
			if parsed, pErr := time.Parse(time.RFC3339Nano, raw); pErr == nil && parsed.After(after) {
				after = parsed.UTC()
			}
		}
		dueTicks, err := collectDueTimerTicks(cond, anchor, after, now, reg.Lifecycle.ExpiresAt, reg.Lifecycle.Repeat)
		if err != nil || len(dueTicks) == 0 {
			continue
		}
		firstTick := dueTicks[0]
		lastTick := dueTicks[len(dueTicks)-1]
		missed := len(dueTicks) - 1
		occID := fmt.Sprintf("timer:%d:%s", i, lastTick.UTC().Format(time.RFC3339Nano))
		ev := map[string]any{
			"kind":         "timer",
			"scheduled_at": lastTick.UTC().Format(time.RFC3339Nano),
			"tick_count":   len(dueTicks),
			"missed_ticks": missed,
		}
		if missed > 0 {
			ev["first_due_at"] = firstTick.UTC().Format(time.RFC3339Nano)
			ev["last_due_at"] = lastTick.UTC().Format(time.RFC3339Nano)
		}
		cursorUpdates := map[string]string{
			timerCursorKey(i): lastTick.UTC().Format(time.RFC3339Nano),
		}
		if _, _, err := s.RecordMatch(ctx, reg.WatchID, MatchInput{
			SourceKind:   "timer",
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     ev,
		}, cursorUpdates); err != nil {
			return err
		}
		if !reg.Lifecycle.Repeat {
			return nil
		}
	}

	rtx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	refreshed, err := loadRegistrationByIDTx(ctx, rtx, watchID)
	_ = rtx.Rollback()
	if err == nil && refreshed.Lifecycle.State == StateLive {
		s.scheduleWatchTimer(refreshed)
	}
	return nil
}

func collectDueTimerTicks(cond awaitstore.Condition, anchor, after, now, expiresAt time.Time, repeat bool) ([]time.Time, error) {
	if !repeat {
		cands, err := NextTimerOccurrences(cond, anchor, after, 1)
		if err != nil || len(cands) == 0 {
			return nil, err
		}
		if !cands[0].After(now) && cands[0].Before(expiresAt) {
			return cands[:1], nil
		}
		return nil, nil
	}
	var due []time.Time
	cur := after
	const maxCatchupBatch = 512
	for len(due) < maxCatchupBatch {
		cands, err := NextTimerOccurrences(cond, anchor, cur, 32)
		if err != nil || len(cands) == 0 {
			break
		}
		stopped := false
		for _, t := range cands {
			if t.After(now) || !t.Before(expiresAt) {
				stopped = true
				break
			}
			due = append(due, t)
			cur = t
			if len(due) >= maxCatchupBatch {
				stopped = true
				break
			}
		}
		if stopped {
			break
		}
	}
	return due, nil
}
