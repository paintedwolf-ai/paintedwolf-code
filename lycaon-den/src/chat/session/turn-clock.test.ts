import { describe, expect, it } from "vitest";
import type { TurnClock } from "../../api/types.ts";
import { isTurnClockAsNew, mergeTurnClocks, turnClockElapsedMs } from "./turn-clock.ts";

const RUNNING_SINCE = "2026-01-02T00:00:00Z";
const SINCE_MS = Date.parse(RUNNING_SINCE);

describe("turnClockElapsedMs", () => {
  it("adds the live span to banked time while running", () => {
    expect(
      turnClockElapsedMs(
        {
          active_ms: 90_000,
          running: true,
          running_at: RUNNING_SINCE,
        },
        SINCE_MS + 5_000,
      ),
    ).toBe(95_000);
  });

  it("freezes at banked time when paused", () => {
    expect(
      turnClockElapsedMs(
        { active_ms: 90_000, running: false },
        SINCE_MS + 5_000,
      ),
    ).toBe(90_000);
  });

  // Clamp clock skew at the banked duration.
  it("never counts backwards under clock skew", () => {
    expect(
      turnClockElapsedMs(
        {
          active_ms: 1_000,
          running: true,
          running_at: RUNNING_SINCE,
        },
        SINCE_MS - 30_000,
      ),
    ).toBe(1_000);
  });

  it("falls back to banked time when running_at is unparseable", () => {
    expect(
      turnClockElapsedMs(
        {
          active_ms: 7_000,
          running: true,
          running_at: "not a date",
        },
        SINCE_MS,
      ),
    ).toBe(7_000);
  });

  it("reads a missing clock as zero", () => {
    expect(turnClockElapsedMs(undefined, SINCE_MS)).toBe(0);
  });
});

const clock = (patch: Partial<TurnClock>): TurnClock => ({
  session_id: "root",
  opening_message_id: "prompt-1",
  active_ms: 1_000,
  work_ms: 1_000,
  running: false,
  ...patch,
});

describe("isTurnClockAsNew", () => {
  it("prefers the later settle over a stale running edge", () => {
    const settled = clock({ active_ms: 9_000, settled_at: "2026-09-12T10:05:00Z" });
    const staleRunning = clock({ running: true, running_at: "2026-09-12T10:00:00Z" });
    expect(isTurnClockAsNew(staleRunning, settled)).toBe(false);
    expect(isTurnClockAsNew(settled, staleRunning)).toBe(true);
  });

  it("lets a continuation resume a settled clock", () => {
    const settled = clock({ active_ms: 9_000, settled_at: "2026-09-12T10:05:00Z" });
    const resumed = { ...settled, running: true, running_at: "2026-09-12T13:00:00Z" };
    expect(isTurnClockAsNew(resumed, settled)).toBe(true);
    const settledAgain = clock({ active_ms: 12_000, settled_at: "2026-09-12T13:00:03Z" });
    expect(isTurnClockAsNew(settledAgain, resumed)).toBe(true);
    expect(isTurnClockAsNew(resumed, settledAgain)).toBe(false);
  });
});

describe("mergeTurnClocks", () => {
  it("keys clocks by opening message and keeps identity when nothing changes", () => {
    const first = clock({ settled_at: "2026-09-12T10:05:00Z" });
    const record = mergeTurnClocks({}, [first, clock({ opening_message_id: undefined })]);
    expect(Object.keys(record)).toEqual(["prompt-1"]);
    expect(mergeTurnClocks(record, [clock({ running: true })])).toBe(record);
  });
});
