import type {
  AgentWatch,
  StandingWatch,
  WatchCondition,
  WatchGate,
} from "../api/types.ts";
import {
  DEFAULT_DAILY_RUN_CAP,
  DEFAULT_MIN_GAP_MINUTES,
  DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
  isValidTimeZone,
  viewerTimeZone,
} from "./watch-format.ts";

const WEEKDAY_ORDER = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
] as const;

type LocalParts = {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  weekday: string;
};

function getLocalParts(ms: number, timeZone: string): LocalParts {
  const fmt = new Intl.DateTimeFormat("en-US", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    weekday: "long",
    hourCycle: "h23",
  });
  let year = 1970;
  let month = 1;
  let day = 1;
  let hour = 0;
  let minute = 0;
  let weekday = "thursday";
  for (const p of fmt.formatToParts(new Date(ms))) {
    if (p.type === "year") year = Number(p.value);
    else if (p.type === "month") month = Number(p.value);
    else if (p.type === "day") day = Number(p.value);
    else if (p.type === "hour") hour = Number(p.value) % 24;
    else if (p.type === "minute") minute = Number(p.value);
    else if (p.type === "weekday") weekday = p.value.toLowerCase();
  }
  return { year, month, day, hour, minute, weekday };
}

function localOffsetMs(ms: number, timeZone: string): number {
  const p = getLocalParts(ms, timeZone);
  const asUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, 0, 0);
  const rounded = Math.floor(ms / 60_000) * 60_000;
  return asUtc - rounded;
}

/** Resolves a local minute in `timeZone` across DST transitions. */
function resolveLocalMinuteInZone(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  timeZone: string,
): number {
  const wallUtc = Date.UTC(year, month - 1, day, hour, minute, 0, 0);
  const earlyGuess = wallUtc - localOffsetMs(wallUtc - 3 * 3_600_000, timeZone);
  const earlyParts = getLocalParts(earlyGuess, timeZone);
  if (
    earlyParts.year === year &&
    earlyParts.month === month &&
    earlyParts.day === day &&
    earlyParts.hour === hour &&
    earlyParts.minute === minute
  ) {
    return earlyGuess;
  }
  const lateGuess = wallUtc - localOffsetMs(wallUtc + 3 * 3_600_000, timeZone);
  const lateParts = getLocalParts(lateGuess, timeZone);
  if (
    lateParts.year === year &&
    lateParts.month === month &&
    lateParts.day === day &&
    lateParts.hour === hour &&
    lateParts.minute === minute
  ) {
    return lateGuess;
  }
  // Nonexistent spring-forward local minute advances to the transition instant.
  let lo = Math.min(earlyGuess, lateGuess) - 3_600_000;
  let hi = Math.max(earlyGuess, lateGuess) + 3_600_000;
  while (hi - lo > 60_000) {
    const mid = Math.floor((lo + hi) / 120_000) * 60_000;
    const mp = getLocalParts(mid, timeZone);
    const midWall = Date.UTC(mp.year, mp.month - 1, mp.day, mp.hour, mp.minute);
    if (midWall < wallUtc) lo = mid;
    else hi = mid;
  }
  return hi;
}

function formatLocalDateTimeInput(ms: number, timeZone: string): string {
  const tz = isValidTimeZone(timeZone) ? timeZone.trim() : "UTC";
  const p = getLocalParts(ms, tz);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${p.year}-${pad(p.month)}-${pad(p.day)}T${pad(p.hour)}:${pad(p.minute)}`;
}

export function formatRunInTimeZone(ms: number, timeZone: string): string {
  const tz = isValidTimeZone(timeZone) ? timeZone.trim() : "UTC";
  return new Intl.DateTimeFormat(undefined, {
    timeZone: tz,
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(ms));
}

function calendarMatchesWeekday(
  days: readonly string[],
  weekday: string,
): boolean {
  if (days.includes("every_day")) return true;
  if (days.includes("weekdays")) {
    return (
      weekday !== "saturday" &&
      weekday !== "sunday" &&
      WEEKDAY_ORDER.includes(weekday as (typeof WEEKDAY_ORDER)[number])
    );
  }
  return days.includes(weekday);
}

/** Computes the next `count` timer occurrences strictly after `nowMs` in `timeZone`. */
export function previewNextRuns(
  conditions: readonly WatchCondition[],
  timeZone: string,
  anchorMs: number,
  nowMs: number,
  count = 3,
): number[] {
  const tz = isValidTimeZone(timeZone) ? timeZone.trim() : "UTC";
  const candidates: number[] = [];

  for (const cond of conditions) {
    if (cond.kind !== "timer") continue;
    if (cond.at) {
      const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(
        cond.at.trim(),
      );
      if (m) {
        const ts = resolveLocalMinuteInZone(
          Number(m[1]),
          Number(m[2]),
          Number(m[3]),
          Number(m[4]),
          Number(m[5]),
          tz,
        );
        if (ts > nowMs) candidates.push(ts);
      }
    } else if (cond.every_minutes != null && cond.every_minutes >= 15) {
      const stepMs = cond.every_minutes * 60_000;
      const base = Number.isFinite(anchorMs) && anchorMs > 0 ? anchorMs : nowMs;
      const elapsed = Math.max(0, nowMs - base);
      const firstIndex = Math.floor(elapsed / stepMs) + 1;
      for (let i = 0; i < count; i += 1) {
        candidates.push(base + (firstIndex + i) * stepMs);
      }
    } else if (cond.calendar && cond.calendar.times.length > 0) {
      const parsedTimes = cond.calendar.times
        .map((t) => /^(\d{2}):(\d{2})$/.exec(t.trim()))
        .filter((m): m is RegExpExecArray => m !== null)
        .map((m) => ({ hour: Number(m[1]), minute: Number(m[2]) }))
        .filter((t) => t.hour >= 0 && t.hour <= 23 && t.minute >= 0 && t.minute <= 59)
        .sort((a, b) => a.hour * 60 + a.minute - (b.hour * 60 + b.minute));
      if (parsedTimes.length === 0) continue;
      const startParts = getLocalParts(nowMs, tz);
      let found = 0;
      for (let dayOffset = 0; dayOffset < 21 && found < count; dayOffset += 1) {
        const probeDate = new Date(
          Date.UTC(startParts.year, startParts.month - 1, startParts.day + dayOffset, 12, 0, 0),
        );
        const dayParts = getLocalParts(probeDate.getTime(), tz);
        if (!calendarMatchesWeekday(cond.calendar.days, dayParts.weekday)) {
          continue;
        }
        for (const t of parsedTimes) {
          const ts = resolveLocalMinuteInZone(
            dayParts.year,
            dayParts.month,
            dayParts.day,
            t.hour,
            t.minute,
            tz,
          );
          if (ts > nowMs) {
            candidates.push(ts);
            found += 1;
            if (found >= count) break;
          }
        }
      }
    }
  }

  candidates.sort((a, b) => a - b);
  const deduped: number[] = [];
  for (const ts of candidates) {
    if (deduped.length === 0 || deduped[deduped.length - 1] !== ts) {
      deduped.push(ts);
      if (deduped.length >= count) break;
    }
  }
  return deduped;
}

type StandingWatchChangedField = "name" | "schedule" | "prompt";

export type StandingWatchEditorSeed = {
  mode: "create" | "edit" | "keep";
  watchId?: string;
  revision?: number;
  keepSource?: { sessionId: string; handle: string };
  name: string;
  prompt: string;
  conditions: WatchCondition[];
  gate: WatchGate | null;
  minGapMinutes: number;
  targetKind: "new_chat" | "existing_chat";
  targetSessionId: string;
  workflowId: string;
  modelId: string;
  runSpendLimitNanoUsd: number;
  allowUnpriced: boolean;
  dailyRunCap: number;
  timeZone: string;
  enableDisabledTakeover?: boolean;
  changedFields: ReadonlySet<StandingWatchChangedField>;
};

export function convertAgentConditionForStanding(
  cond: WatchCondition,
  opts: { repeat: boolean; baseMs: number; timeZone: string },
): WatchCondition {
  if (cond.kind !== "timer") return { ...cond };
  if (cond.delay_ms != null && cond.delay_ms > 0) {
    if (opts.repeat) {
      const mins = Math.min(10_080, Math.max(15, Math.ceil(cond.delay_ms / 60_000)));
      return { kind: "timer", every_minutes: mins };
    }
    return {
      kind: "timer",
      at: formatLocalDateTimeInput(opts.baseMs + cond.delay_ms, opts.timeZone),
    };
  }
  return {
    ...cond,
    ...(cond.calendar
      ? {
          calendar: {
            days: [...cond.calendar.days],
            times: [...cond.calendar.times],
          },
        }
      : {}),
  };
}

export function computeDraftChangedFields(
  draft: NonNullable<AgentWatch["standing_draft"]>,
  convertedConditions: readonly WatchCondition[],
  existing: StandingWatch,
): StandingWatchChangedField[] {
  const out: StandingWatchChangedField[] = [];
  if (draft.name.trim() !== existing.name.trim()) out.push("name");
  if (JSON.stringify(convertedConditions) !== JSON.stringify(existing.conditions)) {
    out.push("schedule");
  }
  if (draft.prompt.trim() !== existing.prompt.trim()) out.push("prompt");
  return out;
}

export function buildEditorSeedFromAgentWatch(
  watch: AgentWatch,
  existingReplaced: StandingWatch | undefined,
  nowMs: number,
): StandingWatchEditorSeed {
  const tz = existingReplaced?.time_zone ?? viewerTimeZone();
  const createdMs = Date.parse(watch.created_at);
  const baseMs = Number.isNaN(createdMs) ? nowMs : createdMs;
  const converted = watch.conditions.map((c) =>
    convertAgentConditionForStanding(c, { repeat: watch.repeat, baseMs, timeZone: tz }),
  );
  const draft = watch.standing_draft;
  if (existingReplaced && draft?.replaces) {
    const changed = computeDraftChangedFields(draft, converted, existingReplaced);
    return {
      mode: "keep",
      watchId: existingReplaced.watch_id,
      revision: existingReplaced.revision,
      keepSource: { sessionId: watch.session_id, handle: watch.handle },
      name: draft.name,
      prompt: draft.prompt,
      conditions: converted,
      gate: existingReplaced.gate ? { ...existingReplaced.gate } : null,
      minGapMinutes: existingReplaced.min_gap_minutes,
      targetKind: existingReplaced.target.kind,
      targetSessionId: existingReplaced.target.session_id ?? "",
      workflowId: existingReplaced.workflow_id,
      modelId: existingReplaced.model_id,
      runSpendLimitNanoUsd: existingReplaced.run_spend_limit_nano_usd,
      allowUnpriced: existingReplaced.allow_unpriced,
      dailyRunCap: existingReplaced.daily_run_cap,
      timeZone: existingReplaced.time_zone,
      changedFields: new Set(changed),
    };
  }
  return {
    mode: "keep",
    keepSource: { sessionId: watch.session_id, handle: watch.handle },
    name: draft?.name ?? "",
    prompt: draft?.prompt ?? "",
    conditions: converted,
    gate: null,
    minGapMinutes: DEFAULT_MIN_GAP_MINUTES,
    targetKind: "new_chat",
    targetSessionId: "",
    workflowId: "default",
    modelId: "",
    runSpendLimitNanoUsd: DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
    allowUnpriced: false,
    dailyRunCap: DEFAULT_DAILY_RUN_CAP,
    timeZone: tz,
    changedFields: new Set(),
  };
}
