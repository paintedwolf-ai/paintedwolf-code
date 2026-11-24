import { LycaonApiError } from "../api/http.ts";
import type {
  StandingWatch,
  WatchCalendarSchedule,
  WatchCondition,
  WatchRun,
} from "../api/types.ts";

export const DEFAULT_RUN_SPEND_LIMIT_NANO_USD = 2_000_000_000;
export const DEFAULT_DAILY_RUN_CAP = 8;
export const DEFAULT_MIN_GAP_MINUTES = 15;

const WEEKDAY_LABELS: Record<string, string> = {
  monday: "Monday",
  tuesday: "Tuesday",
  wednesday: "Wednesday",
  thursday: "Thursday",
  friday: "Friday",
  saturday: "Saturday",
  sunday: "Sunday",
};

export function viewerTimeZone(): string {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (tz && tz.trim()) return tz.trim();
  } catch {
    // Fall back to UTC when Intl lacks a host zone.
  }
  return "UTC";
}

export function isValidTimeZone(tz: string): boolean {
  const trimmed = tz.trim();
  if (!trimmed) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: trimmed });
    return true;
  } catch {
    return false;
  }
}

/** Formats `every_minutes` in the largest exact unit per standing-watches spec. */
export function formatEveryMinutes(minutes: number): string {
  if (minutes === 10_080) return "Every week";
  if (minutes > 0 && minutes % 1_440 === 0) {
    const days = minutes / 1_440;
    return days === 1 ? "Every day" : `Every ${days} days`;
  }
  if (minutes > 0 && minutes % 60 === 0) {
    const hours = minutes / 60;
    return hours === 1 ? "Every hour" : `Every ${hours} hours`;
  }
  return `Every ${minutes} minutes`;
}

function formatCalendarSchedule(
  cal: WatchCalendarSchedule,
  timeZone?: string,
): string {
  let daysLabel = "Selected days";
  if (cal.days.includes("every_day")) {
    daysLabel = "Every day";
  } else if (cal.days.includes("weekdays")) {
    daysLabel = "Weekdays";
  } else if (cal.days.length > 0) {
    daysLabel = cal.days.map((d) => WEEKDAY_LABELS[d] ?? d).join(", ");
  }
  const timesLabel = cal.times.join(", ");
  const zoneSuffix = timeZone?.trim() ? ` (${timeZone.trim()})` : "";
  return `${daysLabel} at ${timesLabel}${zoneSuffix}`;
}

function formatWatchCondition(
  cond: WatchCondition,
  opts?: { timeZone?: string; repeat?: boolean },
): string {
  const tz = opts?.timeZone?.trim();
  switch (cond.kind) {
    case "timer": {
      if (cond.calendar) return formatCalendarSchedule(cond.calendar, tz);
      if (cond.every_minutes != null && cond.every_minutes > 0) {
        return formatEveryMinutes(cond.every_minutes);
      }
      if (cond.at) {
        const suffix = tz ? ` (${tz})` : "";
        return `At ${cond.at.replace("T", " ")}${suffix}`;
      }
      if (cond.delay_ms != null && cond.delay_ms > 0) {
        if (cond.delay_ms % 60_000 === 0) {
          const mins = cond.delay_ms / 60_000;
          return opts?.repeat
            ? formatEveryMinutes(mins)
            : mins === 1
              ? "In 1 minute"
              : `In ${mins} minutes`;
        }
        const secs = Math.round(cond.delay_ms / 1_000);
        return opts?.repeat ? `Every ${secs}s` : `In ${secs}s`;
      }
      return "Timer";
    }
    case "git_ref":
      return `When \`${cond.ref ?? "HEAD"}\` changes`;
    case "file_change":
      return `When files in \`${cond.path || "."}\` change`;
    case "process_done":
      return cond.process_id
        ? `When command ${cond.process_id} finishes`
        : "When command finishes";
    case "process_output":
      return cond.process_id
        ? `When command ${cond.process_id} writes output`
        : "When command writes output";
    case "http_ready":
      return `When ${cond.url ?? "endpoint"} responds`;
    case "port_ready":
      return `When ${cond.host ?? "127.0.0.1"}:${cond.port ?? 0} opens`;
    case "next_worker_done":
      return "When next worker finishes";
    case "all_workers_idle":
      return "When all workers are idle";
    case "worker_decision_needed":
      return "When a worker needs a decision";
    case "worker_failed":
      return "When a worker fails";
    case "overlay_promote_pending":
      return "When worker changes are ready";
    case "scan_done":
      return "When scan finishes";
  }
}

export function formatWatchConditionsSummary(
  conditions: readonly WatchCondition[],
  opts?: { timeZone?: string; repeat?: boolean },
): string {
  if (conditions.length === 0) return "No conditions";
  return conditions.map((c) => formatWatchCondition(c, opts)).join(" or ");
}

export function formatNanoUsdShort(nanoUsd: number): string {
  const usd = nanoUsd / 1_000_000_000;
  if (Number.isInteger(usd)) return `$${usd}`;
  const two = usd.toFixed(2);
  return `$${two.endsWith("0") ? two.slice(0, -1) : two}`;
}

export function formatActivitySpendHeader(
  standingWatches: readonly StandingWatch[],
  hasUnpricedFromHost?: boolean,
): string | null {
  const activeList = standingWatches.filter((w) => w.state !== "completed");
  if (activeList.length === 0) return null;
  const enabled = activeList.filter((w) => w.state === "enabled");
  if (
    hasUnpricedFromHost === true ||
    enabled.some((w) => w.allow_unpriced === true)
  ) {
    return "No daily spend limit";
  }
  const totalNano = enabled.reduce(
    (sum, w) => sum + w.daily_run_cap * w.run_spend_limit_nano_usd,
    0,
  );
  const count = activeList.length;
  const noun = count === 1 ? "standing watch" : "standing watches";
  return `Up to ${formatNanoUsdShort(totalNano)} a day across ${count} ${noun}`;
}

export function formatStandingStateLabel(
  state: StandingWatch["state"],
  reason?: StandingWatch["state_reason"],
): string {
  switch (state) {
    case "enabled":
      return "Enabled";
    case "paused":
      if (reason === "consecutive_failures") return "Paused · 3 failed runs";
      if (reason === "target_unavailable") return "Paused · target chat closed";
      return "Paused";
    case "disabled":
      return "Disabled · owner access removed";
    case "completed":
      return "Completed";
  }
}

export function formatWatchRunStatus(status: WatchRun["status"]): string {
  switch (status) {
    case "pending":
      return "Pending";
    case "queued":
      return "Queued";
    case "running":
      return "Running";
    case "waiting_for_owner":
      return "Waiting on approval";
    case "completed":
      return "Completed";
    case "failed":
      return "Failed";
    case "fenced":
      return "Spend limit reached";
    case "canceled":
      return "Canceled";
    case "skipped_overlap":
      return "Skipped · prior run active";
    case "skipped_unchanged":
      return "Skipped · gate unchanged";
    case "skipped_daily_cap":
      return "Skipped · daily cap reached";
    case "skipped_busy":
      return "Skipped · target chat busy";
    case "missed":
      return "Missed while closed";
  }
}

export function formatFirstPromptLine(prompt: string, maxChars = 96): string {
  const first = (prompt.split(/\r?\n/, 1)[0] ?? "").trim();
  if (first.length <= maxChars) return first;
  return `${first.slice(0, Math.max(1, maxChars - 1)).trimEnd()}…`;
}

export function formatHostWatchError(err: unknown): string {
  if (err instanceof LycaonApiError) {
    const title = err.title?.trim();
    const msg = err.message.trim();
    if (title && msg && !msg.startsWith(title)) return `${title}: ${msg}`;
    return msg || title || "Watch action failed.";
  }
  return err instanceof Error ? err.message : String(err);
}
