import { type TimelineManifest, type TimelineSummary, timelineClock } from "./timeline-archive.ts";

export type TimelineTone = "neutral" | "muted" | "warning" | "danger";

/** One moment or span on a recording's lanes; selecting it seeks there. */
export type TimelineMarker = {
  lane: "action" | "page";
  atMs: number;
  /** Set for a span, such as an action that took time. */
  endMs?: number;
  tone: TimelineTone;
  label: string;
};

/** One line of what the host measured, with the moment it points to when there is one. */
export type TimelineFact = {
  text: string;
  tone: TimelineTone;
  atMs?: number;
};

const num = (value: unknown): number | undefined => (typeof value === "number" && Number.isFinite(value) ? value : undefined);
const str = (value: unknown): string => (typeof value === "string" ? value.trim() : "");

/** Actions, and the page events a reader looks for: unexpected shifts, stalls, failures, errors, and watched jumps. */
export function timelineMarkers(manifest: TimelineManifest): TimelineMarker[] {
  const markers: TimelineMarker[] = manifest.actions.map((action) => ({
    lane: "action",
    atMs: action.start_ms,
    endMs: Math.max(action.start_ms, action.end_ms),
    tone: action.ok ? "neutral" : "danger",
    label: action.ok ? action.label : `${action.label} failed`,
  }));
  for (const event of manifest.events) {
    const detail = event.detail ?? {};
    switch (event.kind) {
      case "layout_shift": {
        const value = num(detail.value) ?? 0;
        const input = detail.had_recent_input === true;
        markers.push({
          lane: "page",
          atMs: event.at_ms,
          tone: input ? "muted" : "warning",
          label: `Layout shift ${value.toFixed(3)}${input ? " after input" : ""}`,
        });
        break;
      }
      case "long_task":
        markers.push({ lane: "page", atMs: event.at_ms, tone: "muted", label: `Long task ${Math.round(num(detail.duration_ms) ?? 0)} ms` });
        break;
      case "request": {
        const status = num(detail.status);
        const failure = str(detail.failure);
        if (!failure && (status === undefined || status < 400)) break;
        const target = [str(detail.method), str(detail.url)].filter(Boolean).join(" ");
        markers.push({ lane: "page", atMs: event.at_ms, tone: "danger", label: `${target || "Request"} ${failure || String(status)}` });
        break;
      }
      case "error":
        markers.push({ lane: "page", atMs: event.at_ms, tone: "danger", label: str(detail.message) || "Uncaught error" });
        break;
    }
  }
  for (const watch of manifest.summary.watch ?? []) {
    for (const jump of watch.jumps ?? []) {
      markers.push({ lane: "page", atMs: jump.at_ms, tone: "warning", label: `${watch.selector} ${describeJump(jump)}` });
    }
  }
  return markers.sort((a, b) => a.atMs - b.atMs);
}

function describeJump(jump: { dx?: number; dy?: number; dw?: number; dh?: number; scroll?: number }): string {
  const parts: string[] = [];
  const px = (n: number) => `${Math.round(Math.abs(n))} px`;
  if (jump.dy) parts.push(`moved ${px(jump.dy)} ${jump.dy > 0 ? "down" : "up"}`);
  if (jump.dx) parts.push(`moved ${px(jump.dx)} ${jump.dx > 0 ? "right" : "left"}`);
  if (jump.dw || jump.dh) parts.push("resized");
  if (jump.scroll) parts.push(`scrolled ${px(jump.scroll)}`);
  return parts.join(", ") || "changed";
}

/** The summary as a short list, most telling first. */
export function timelineFacts(summary: TimelineSummary): TimelineFact[] {
  const facts: TimelineFact[] = [];
  facts.push(summary.still_changing_at_end
    ? { text: "Still changing when recording stopped", tone: "warning", atMs: summary.duration_ms }
    : { text: `Stable from ${timelineClock(summary.visually_stable_at_ms)}`, tone: "neutral", atMs: summary.visually_stable_at_ms });
  const shift = summary.layout_shift;
  if (shift.count > 0) {
    facts.push({
      text: `Unexpected layout shift ${shift.total.toFixed(3)} (${shift.count})`,
      tone: "warning",
      atMs: shift.worst?.at_ms,
    });
  }
  if (shift.after_input.count > 0) {
    facts.push({ text: `Shift after input ${shift.after_input.total.toFixed(3)}`, tone: "muted" });
  }
  for (const watch of summary.watch ?? []) {
    const last = watch.jumps?.at(-1);
    if (!watch.present) facts.push({ text: `${watch.selector} gone at the end`, tone: "warning" });
    else if (last) facts.push({ text: `${watch.selector} moved up to ${Math.round(watch.max_displacement_px)} px`, tone: "warning", atMs: last.at_ms });
  }
  if ((summary.failed_requests ?? 0) > 0) {
    facts.push({ text: plural(summary.failed_requests ?? 0, "failed request"), tone: "danger" });
  }
  if ((summary.errors ?? 0) > 0) {
    facts.push({ text: plural(summary.errors ?? 0, "error"), tone: "danger" });
  }
  if (summary.long_tasks.count > 0) {
    facts.push({ text: `${plural(summary.long_tasks.count, "long task")}, longest ${Math.round(summary.long_tasks.max_ms)} ms`, tone: "muted" });
  }
  return facts;
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/** What a moment shows, for the scrubber's spoken value: the time and the last action begun by then. */
export function timelineMomentText(manifest: TimelineManifest, atMs: number): string {
  const action = manifest.actions.filter((a) => a.start_ms <= atMs).at(-1);
  return action ? `${timelineClock(atMs)}, after ${action.label}` : timelineClock(atMs);
}
