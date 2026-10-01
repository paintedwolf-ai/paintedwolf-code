/**
 * Day labels and clock times in the viewer's locale and zone. Times under an
 * hour old read relative; older ones read as the clock time.
 */

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

export type CalendarZone = {
  locale?: string;
  timeZone?: string;
};

export type CalendarTimeFormat = {
  /** Calendar day in the zone, as consecutive integers (days since the epoch). */
  dayKey: (at: number) => number;
  /** Today, Yesterday, a weekday within the week, then a date. */
  dayLabel: (at: number, now: number) => string;
  /** The full date a day label stands for. */
  fullDate: (at: number) => string;
  /** Full date and clock time. */
  fullDateTime: (at: number) => string;
  /** Clock time alone. */
  time: (at: number) => string;
  /** Just now, minutes ago under an hour, then the clock time. */
  stamp: (at: number, now: number) => string;
  /** When a turn's work finished, in the same register as `stamp`. */
  finished: (at: number, now: number) => string;
};

/** Builds one formatter per zone; Intl formatters are costly to construct. */
export function createCalendarTimeFormat(zone: CalendarZone = {}): CalendarTimeFormat {
  const { locale, timeZone } = zone;
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    year: "numeric",
    month: "numeric",
    day: "numeric",
  });
  const clock = new Intl.DateTimeFormat(locale, { timeZone, hour: "numeric", minute: "2-digit" });
  const weekday = new Intl.DateTimeFormat(locale, { timeZone, weekday: "long" });
  const monthDay = new Intl.DateTimeFormat(locale, {
    timeZone,
    weekday: "short",
    month: "short",
    day: "numeric",
  });
  const monthDayYear = new Intl.DateTimeFormat(locale, {
    timeZone,
    month: "short",
    day: "numeric",
    year: "numeric",
  });
  const fullDate = new Intl.DateTimeFormat(locale, { timeZone, dateStyle: "full" });
  const fullDateTime = new Intl.DateTimeFormat(locale, {
    timeZone,
    dateStyle: "full",
    timeStyle: "short",
  });

  const calendar = (at: number): { year: number; day: number } => {
    let year = 0;
    let month = 0;
    let date = 0;
    for (const part of parts.formatToParts(at)) {
      if (part.type === "year") year = Number(part.value);
      else if (part.type === "month") month = Number(part.value);
      else if (part.type === "day") date = Number(part.value);
    }
    return { year, day: Math.round(Date.UTC(year, month - 1, date) / DAY_MS) };
  };

  const dayLabel = (at: number, now: number): string => {
    const then = calendar(at);
    const today = calendar(now);
    const daysAgo = today.day - then.day;
    if (daysAgo === 0) return "Today";
    if (daysAgo === 1) return "Yesterday";
    if (daysAgo > 1 && daysAgo < 7) return weekday.format(at);
    if (then.year === today.year) return monthDay.format(at);
    return monthDayYear.format(at);
  };

  const relative = (at: number, now: number): string | null => {
    const age = now - at;
    if (age < MINUTE_MS) return "just now";
    if (age < HOUR_MS) return `${Math.floor(age / MINUTE_MS)}m ago`;
    return null;
  };

  return {
    dayKey: (at) => calendar(at).day,
    dayLabel,
    fullDate: (at) => fullDate.format(at),
    fullDateTime: (at) => fullDateTime.format(at),
    time: (at) => clock.format(at),
    stamp: (at, now) => {
      const recent = relative(at, now);
      if (recent === null) return clock.format(at);
      return recent === "just now" ? "Just now" : recent;
    },
    finished: (at, now) => {
      const recent = relative(at, now);
      return recent === null
        ? `Finished at ${clock.format(at)}`
        : `Finished ${recent}`;
    },
  };
}

let viewerFormat: CalendarTimeFormat | undefined;

/** The formatter for the viewer's own locale and zone. */
export function viewerCalendarTimeFormat(): CalendarTimeFormat {
  viewerFormat ??= createCalendarTimeFormat();
  return viewerFormat;
}
