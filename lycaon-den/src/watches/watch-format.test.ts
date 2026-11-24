import { describe, expect, it } from "vitest";
import type { AgentWatch, StandingWatch } from "../api/types.ts";
import {
  formatActivitySpendHeader,
  formatEveryMinutes,
  formatWatchConditionsSummary,
} from "./watch-format.ts";
import {
  buildEditorSeedFromAgentWatch,
  previewNextRuns,
} from "./watch-schedule-preview.ts";

describe("watch formatting and schedule preview", () => {
  it("formats every_minutes in the largest exact unit", () => {
    expect(formatEveryMinutes(15)).toBe("Every 15 minutes");
    expect(formatEveryMinutes(30)).toBe("Every 30 minutes");
    expect(formatEveryMinutes(60)).toBe("Every hour");
    expect(formatEveryMinutes(90)).toBe("Every 90 minutes");
    expect(formatEveryMinutes(120)).toBe("Every 2 hours");
    expect(formatEveryMinutes(1_440)).toBe("Every day");
    expect(formatEveryMinutes(2_880)).toBe("Every 2 days");
    expect(formatEveryMinutes(10_080)).toBe("Every week");
  });

  it("summarizes calendar, git_ref, and file_change conditions joined with or", () => {
    expect(
      formatWatchConditionsSummary(
        [
          {
            kind: "timer",
            calendar: { days: ["weekdays"], times: ["09:00"] },
          },
          { kind: "git_ref", ref: "refs/heads/main" },
          { kind: "file_change", path: "src/" },
        ],
        { timeZone: "America/Los_Angeles", repeat: true },
      ),
    ).toBe(
      "Weekdays at 09:00 (America/Los_Angeles) or When `refs/heads/main` changes or When files in `src/` change",
    );
  });

  it("computes the daily spend header and unpriced override", () => {
    const base: StandingWatch = {
      watch_id: "00000000-0000-4000-8000-000000000001",
      project_id: "00000000-0000-4000-8000-000000000010",
      owner_person_id: "00000000-0000-4000-8000-000000000020",
      name: "Watch A",
      prompt: "Run checks",
      conditions: [{ kind: "timer", every_minutes: 60 }],
      condition_summary: "Every hour",
      min_gap_minutes: 15,
      target: { kind: "new_chat" },
      workflow_id: "default",
      model_id: "mock:fast",
      run_spend_limit_nano_usd: 2_000_000_000,
      allow_unpriced: false,
      daily_run_cap: 8,
      time_zone: "UTC",
      state: "enabled",
      revision: 1,
      created_at: "2026-04-20T00:00:00Z",
      updated_at: "2026-04-20T00:00:00Z",
    };
    const paused: StandingWatch = {
      ...base,
      watch_id: "00000000-0000-4000-8000-000000000002",
      state: "paused",
      state_reason: "person",
    };

    expect(formatActivitySpendHeader([])).toBeNull();
    expect(formatActivitySpendHeader([base, paused])).toBe(
      "Up to $16 a day across 2 standing watches",
    );
    expect(
      formatActivitySpendHeader([{ ...base, allow_unpriced: true }]),
    ).toBe("No daily spend limit");
  });

  it("previews next 3 run times in IANA time zones including DST transitions", () => {
    // 2026-03-08 02:30 America/Los_Angeles springs forward to 03:00 PDT (10:00Z).
    const springSkipped = previewNextRuns(
      [{ kind: "timer", at: "2026-03-08T02:30" }],
      "America/Los_Angeles",
      Date.parse("2026-03-08T08:00:00Z"),
      Date.parse("2026-03-08T08:00:00Z"),
      1,
    );
    expect(springSkipped.map((t) => new Date(t).toISOString())).toEqual([
      "2026-03-08T10:00:00.000Z",
    ]);

    // 2026-11-01 01:30 America/Los_Angeles repeats; picks earlier PDT occurrence (08:30Z).
    const fallRepeated = previewNextRuns(
      [{ kind: "timer", at: "2026-11-01T01:30" }],
      "America/Los_Angeles",
      Date.parse("2026-11-01T07:00:00Z"),
      Date.parse("2026-11-01T07:00:00Z"),
      1,
    );
    expect(fallRepeated.map((t) => new Date(t).toISOString())).toEqual([
      "2026-11-01T08:30:00.000Z",
    ]);

    const nowMs = Date.parse("2026-04-20T08:00:00Z"); // Mon 08:00 UTC
    const next = previewNextRuns(
      [{ kind: "timer", calendar: { days: ["weekdays"], times: ["09:00"] } }],
      "UTC",
      nowMs,
      nowMs,
      3,
    );
    expect(next.map((t) => new Date(t).toISOString())).toEqual([
      "2026-04-20T09:00:00.000Z",
      "2026-04-21T09:00:00.000Z",
      "2026-04-22T09:00:00.000Z",
    ]);
  });

  it("converts repeating and one-shot delay_ms timers and marks changed fields for replaces", () => {
    const createdAt = "2026-04-20T10:00:00Z";
    const repeatingAgent: AgentWatch = {
      watch_id: "00000000-0000-4000-8000-000000000101",
      handle: "watch-1",
      owner_kind: "session",
      project_id: "00000000-0000-4000-8000-000000000010",
      session_id: "00000000-0000-4000-8000-000000000030",
      conditions: [{ kind: "timer", delay_ms: 120_000 }],
      condition_summary: "Every 2 minutes",
      delivery: "collect",
      repeat: true,
      derived: false,
      keepable: true,
      state: "live",
      revision: 1,
      expires_at: "2026-04-27T10:00:00Z",
      created_at: createdAt,
      updated_at: createdAt,
    };

    const seedRepeat = buildEditorSeedFromAgentWatch(
      repeatingAgent,
      undefined,
      Date.parse(createdAt),
    );
    expect(seedRepeat.conditions[0]).toEqual({
      kind: "timer",
      every_minutes: 15,
    });

    const existing: StandingWatch = {
      watch_id: "00000000-0000-4000-8000-000000000201",
      project_id: "00000000-0000-4000-8000-000000000010",
      owner_person_id: "00000000-0000-4000-8000-000000000020",
      name: "Nightly check",
      prompt: "Old prompt",
      conditions: [{ kind: "timer", every_minutes: 60 }],
      condition_summary: "Every hour",
      min_gap_minutes: 15,
      target: { kind: "new_chat" },
      workflow_id: "default",
      model_id: "mock:fast",
      run_spend_limit_nano_usd: 2_000_000_000,
      allow_unpriced: false,
      daily_run_cap: 8,
      time_zone: "UTC",
      state: "enabled",
      revision: 4,
      created_at: createdAt,
      updated_at: createdAt,
    };

    const replacesSeed = buildEditorSeedFromAgentWatch(
      {
        ...repeatingAgent,
        conditions: [{ kind: "timer", every_minutes: 120 }],
        standing_draft: {
          name: "Nightly check",
          prompt: "Updated prompt",
          replaces: existing.watch_id,
        },
      },
      existing,
      Date.parse(createdAt),
    );
    expect(replacesSeed.revision).toBe(4);
    expect([...replacesSeed.changedFields]).toEqual(["schedule", "prompt"]);
  });
});
