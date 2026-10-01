import { describe, expect, it } from "vitest";
import {
  formatDuration,
  formatElapsed,
  formatRelativeTime,
  parseInstant,
  relativeTimeLabel,
} from "./time-copy.ts";

const DAY_MS = 24 * 60 * 60 * 1000;

describe("formatRelativeTime", () => {
  it("uses days for recent dates", () => {
    const now = 400 * DAY_MS;
    expect(formatRelativeTime(now - 29 * DAY_MS, now)).toBe("29d ago");
  });

  it("uses months once a date is at least 30 days old", () => {
    const now = 400 * DAY_MS;
    expect(formatRelativeTime(now - 30 * DAY_MS, now)).toBe("1mo ago");
    expect(formatRelativeTime(now - 364 * DAY_MS, now)).toBe("12mo ago");
  });

  it("uses years once a date is at least 365 days old", () => {
    const now = 800 * DAY_MS;
    expect(formatRelativeTime(now - 365 * DAY_MS, now)).toBe("1y ago");
    expect(formatRelativeTime(now - 730 * DAY_MS, now)).toBe("2y ago");
  });

  it("applies the same units to ISO timestamps", () => {
    const now = Date.parse("2026-08-27T12:00:00Z");
    expect(relativeTimeLabel("2026-06-28T12:00:00Z", now)).toBe("2mo ago");
    expect(relativeTimeLabel("2024-08-27T12:00:00Z", now)).toBe("2y ago");
    expect(relativeTimeLabel("not a date", now)).toBe("");
  });
});

describe("formatElapsed", () => {
  it("renders m:ss under an hour", () => {
    expect(formatElapsed(0)).toBe("0:00");
    expect(formatElapsed(4_000)).toBe("0:04");
    expect(formatElapsed(95_000)).toBe("1:35");
    expect(formatElapsed(59 * 60_000 + 59_000)).toBe("59:59");
  });

  it("grows to h:mm:ss past an hour", () => {
    expect(formatElapsed(3_600_000)).toBe("1:00:00");
    expect(formatElapsed(3_600_000 + 5 * 60_000 + 7_000)).toBe("1:05:07");
  });

  it("floors partial seconds rather than rounding up", () => {
    expect(formatElapsed(1_999)).toBe("0:01");
  });
});

describe("formatDuration", () => {
  it("uses the two largest units that apply", () => {
    expect(formatDuration(0)).toBe("0s");
    expect(formatDuration(45_000)).toBe("45s");
    expect(formatDuration(60_000)).toBe("1m 0s");
    expect(formatDuration(204_000)).toBe("3m 24s");
    expect(formatDuration(3_900_000)).toBe("1h 5m");
  });

  it("rounds to the nearest second and never goes negative", () => {
    expect(formatDuration(1_499)).toBe("1s");
    expect(formatDuration(59_600)).toBe("1m 0s");
    expect(formatDuration(-5_000)).toBe("0s");
  });
});

describe("parseInstant", () => {
  it("reads RFC 3339 and rejects blanks and junk", () => {
    expect(parseInstant("2026-09-12T10:00:00Z")).toBe(Date.parse("2026-09-12T10:00:00Z"));
    expect(parseInstant("")).toBeNull();
    expect(parseInstant(undefined)).toBeNull();
    expect(parseInstant("yesterday")).toBeNull();
  });
});
