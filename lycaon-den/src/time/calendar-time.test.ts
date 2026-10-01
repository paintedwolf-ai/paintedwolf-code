import { describe, expect, it } from "vitest";
import { createCalendarTimeFormat } from "./calendar-time.ts";

const format = createCalendarTimeFormat({ locale: "en-US", timeZone: "America/Los_Angeles" });
// Intl may separate the clock time from AM/PM with a narrow no-break space.
const plain = (value: string) => value.replace(/ /g, " ");
const at = (iso: string) => Date.parse(iso);
// Saturday, September 12, 2026, 3:42 PM in Los Angeles.
const NOW = at("2026-09-12T22:42:00Z");

describe("calendar day labels", () => {
  it("names today, yesterday, and weekdays within the week", () => {
    expect(format.dayLabel(at("2026-09-12T07:30:00Z"), NOW)).toBe("Today");
    expect(format.dayLabel(at("2026-09-12T06:59:00Z"), NOW)).toBe("Yesterday");
    expect(format.dayLabel(at("2026-09-10T23:16:00Z"), NOW)).toBe("Thursday");
    expect(format.dayLabel(at("2026-09-06T18:00:00Z"), NOW)).toBe("Sunday");
  });

  it("dates older days, with the year only outside this one", () => {
    expect(format.dayLabel(at("2026-09-05T18:00:00Z"), NOW)).toBe("Sat, Sep 5");
    expect(format.dayLabel(at("2025-08-28T22:42:00Z"), NOW)).toBe("Aug 28, 2025");
  });

  it("keys days on the viewer's calendar, not UTC", () => {
    const lateEvening = at("2026-09-12T06:30:00Z"); // Sep 11, 11:30 PM local
    const nextMorning = at("2026-09-12T15:00:00Z"); // Sep 12, 8:00 AM local
    expect(format.dayKey(nextMorning) - format.dayKey(lateEvening)).toBe(1);
    expect(format.dayKey(at("2026-09-12T23:59:00Z"))).toBe(format.dayKey(nextMorning));
  });

  it("counts calendar days across a daylight saving change", () => {
    const beforeShift = at("2026-11-01T06:30:00Z"); // Oct 31, 11:30 PM PDT
    const afterShift = at("2026-11-02T08:30:00Z"); // Nov 2, 12:30 AM PST
    expect(format.dayKey(afterShift) - format.dayKey(beforeShift)).toBe(2);
  });
});

describe("clock copy", () => {
  it("reads relative under an hour, then the clock time", () => {
    expect(format.stamp(NOW - 20_000, NOW)).toBe("Just now");
    expect(format.stamp(NOW - 18 * 60_000, NOW)).toBe("18m ago");
    expect(plain(format.stamp(NOW - 2 * 3_600_000, NOW))).toBe("1:42 PM");
  });

  it("says when a turn finished in the same register", () => {
    expect(format.finished(NOW - 5_000, NOW)).toBe("Finished just now");
    expect(format.finished(NOW - 59 * 60_000, NOW)).toBe("Finished 59m ago");
    expect(plain(format.finished(at("2026-09-10T23:16:00Z"), NOW))).toBe("Finished at 4:16 PM");
  });

  it("spells out the full date for tooltips", () => {
    expect(format.fullDate(at("2026-09-10T23:16:00Z"))).toBe("Thursday, September 10, 2026");
    expect(plain(format.fullDateTime(at("2026-09-10T23:16:00Z")))).toBe(
      "Thursday, September 10, 2026 at 4:16 PM",
    );
  });
});
