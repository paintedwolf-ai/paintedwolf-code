import { describe, expect, it } from "vitest";
import { APP_STATE_VERSION } from "../../shared/app-state-types.ts";
import { LycaonApiError } from "../api/http.ts";
import type { AgentWatch, StandingWatch } from "../api/types.ts";
import { parseAppState } from "../platform/persistence/app-state-parse.ts";
import {
  formatActivitySpendHeader,
  formatEveryMinutes,
  formatFirstPromptLine,
  formatHostWatchError,
  formatNanoUsdShort,
  formatStandingStateLabel,
  formatWatchConditionsSummary,
  formatWatchRunStatus,
  isValidTimeZone,
  viewerTimeZone,
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

  it("covers all condition kinds, timer variants, state labels, run statuses, and error formatting", () => {
    expect(viewerTimeZone().length).toBeGreaterThan(0);
    expect(isValidTimeZone("")).toBe(false);
    expect(isValidTimeZone("America/New_York")).toBe(true);
    expect(isValidTimeZone("Invalid/Not_A_Zone")).toBe(false);

    expect(formatWatchConditionsSummary([])).toBe("No conditions");
    expect(
      formatWatchConditionsSummary(
        [
          {
            kind: "timer",
            calendar: { days: ["every_day"], times: ["08:00", "20:00"] },
          },
          {
            kind: "timer",
            calendar: { days: ["monday", "friday"], times: ["10:30"] },
          },
          { kind: "timer", at: "2026-05-01T14:00" },
          { kind: "timer", delay_ms: 60_000 },
          { kind: "timer", delay_ms: 180_000 },
          { kind: "timer", delay_ms: 45_000 },
          { kind: "timer" },
        ],
        { timeZone: "UTC", repeat: false },
      ),
    ).toBe(
      "Every day at 08:00, 20:00 (UTC) or Monday, Friday at 10:30 (UTC) or At 2026-05-01 14:00 (UTC) or In 1 minute or In 3 minutes or In 45s or Timer",
    );

    expect(
      formatWatchConditionsSummary(
        [
          { kind: "timer", delay_ms: 60_000 },
          { kind: "timer", delay_ms: 30_000 },
        ],
        { repeat: true },
      ),
    ).toBe("Every minute" === formatEveryMinutes(1) ? "Every minute or Every 30s" : "Every 1 minutes or Every 30s");

    expect(
      formatWatchConditionsSummary([
        { kind: "process_done", process_id: "proc-1" },
        { kind: "process_done" },
        { kind: "process_output", process_id: "proc-2" },
        { kind: "process_output" },
        { kind: "http_ready", url: "http://127.0.0.1:8080/ready" },
        { kind: "port_ready", host: "127.0.0.1", port: 5432 },
        { kind: "next_worker_done" },
        { kind: "all_workers_idle" },
        { kind: "worker_decision_needed" },
        { kind: "worker_failed" },
        { kind: "overlay_promote_pending" },
        { kind: "scan_done" },
      ]),
    ).toContain("When command proc-1 finishes or When command finishes");

    expect(formatNanoUsdShort(2_000_000_000)).toBe("$2");
    expect(formatNanoUsdShort(2_500_000_000)).toBe("$2.5");
    expect(formatNanoUsdShort(1_250_000_000)).toBe("$1.25");

    expect(formatStandingStateLabel("enabled")).toBe("Enabled");
    expect(formatStandingStateLabel("paused", "consecutive_failures")).toBe(
      "Paused · 3 failed runs",
    );
    expect(formatStandingStateLabel("paused", "target_unavailable")).toBe(
      "Paused · target chat closed",
    );
    expect(formatStandingStateLabel("paused", "person")).toBe("Paused");
    expect(formatStandingStateLabel("disabled")).toBe(
      "Disabled · owner access removed",
    );
    expect(formatStandingStateLabel("completed")).toBe("Completed");

    const statuses = [
      "pending",
      "queued",
      "running",
      "waiting_for_owner",
      "completed",
      "failed",
      "fenced",
      "canceled",
      "skipped_overlap",
      "skipped_unchanged",
      "skipped_daily_cap",
      "skipped_busy",
      "missed",
    ] as const;
    expect(statuses.map((s) => formatWatchRunStatus(s))).toEqual([
      "Pending",
      "Queued",
      "Running",
      "Waiting on approval",
      "Completed",
      "Failed",
      "Spend limit reached",
      "Canceled",
      "Skipped · prior run active",
      "Skipped · gate unchanged",
      "Skipped · daily cap reached",
      "Skipped · target chat busy",
      "Missed while closed",
    ]);

    expect(formatFirstPromptLine("  First line\nSecond line", 40)).toBe(
      "First line",
    );
    expect(formatFirstPromptLine("1234567890abcdef", 10)).toBe("123456789…");

    expect(
      formatHostWatchError(
        new LycaonApiError("Daily cap reached.", 409, "watch_daily_cap_reached", {
          title: "Cap reached",
        }),
      ),
    ).toBe("Cap reached: Daily cap reached.");
    expect(formatHostWatchError(new Error("boom"))).toBe("boom");
    expect(formatHostWatchError("raw-err")).toBe("raw-err");
  });

  it("parses and sanitizes persisted hiddenProposalOverflows in app state", () => {
    const parsedValid = parseAppState({
      version: APP_STATE_VERSION,
      recents: [],
      hiddenProposalOverflows: {
        " sess-a ": " watch-3 ",
        "": "ignored",
        "sess-empty": "   ",
        "sess-num": 123,
      },
    });
    expect(parsedValid.hiddenProposalOverflows).toEqual({
      "sess-a": "watch-3",
    });

    const parsedArray = parseAppState({
      version: APP_STATE_VERSION,
      recents: [],
      hiddenProposalOverflows: ["not-a-map"],
    });
    expect(parsedArray.hiddenProposalOverflows).toBeUndefined();
  });
});
