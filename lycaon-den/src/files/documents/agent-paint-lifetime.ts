/** Decides which agent marks a document paints and how strongly, over time. */
import type { WindowColor } from "../../contributions/window-color-palette.ts";
import { AGENT_LINGER_MS, type AgentMark } from "../components/agent-presence.ts";

export const AGENT_FADE_IN_MS = 700;
export const AGENT_FADE_OUT_MS = 1600;
/** Past search matches a chat keeps in the text of one document. */
export const AGENT_PAST_MARKS_MAX = 8;

export type PaintedAgentMark = {
  mark: AgentMark;
  from: number;
  to: number;
  color: WindowColor | undefined;
  bornAt: number;
  /** When the host stopped reporting the mark; null while reported. */
  leftAt: number | null;
  /** When a still-reported match stopped being its chat's current step. */
  supersededAt: number | null;
};

export type ReportedAgentMark = { mark: AgentMark; from: number; to: number; color: WindowColor | undefined };

/**
 * `live` is reported and current. A search match that ended is `past` for the
 * linger time, then every ended mark is `leaving` for the fade. `gone` marks
 * are no longer drawn in the text.
 */
export type AgentMarkPhase = "live" | "past" | "leaving" | "gone";

/** Matches are the only reads the text draws as ranges; other reads tint the gutter for the whole turn. */
function lingers(mark: AgentMark): boolean {
  return mark.kind === "match";
}

/** When the mark stopped being live, or null while it is. */
function endedAt(painted: PaintedAgentMark): number | null {
  if (!lingers(painted.mark)) return painted.leftAt;
  if (painted.leftAt === null) return painted.supersededAt;
  return painted.supersededAt === null ? painted.leftAt : Math.min(painted.leftAt, painted.supersededAt);
}

/** When the fade out starts, or null while the mark is live. */
export function agentMarkLeavingAt(painted: PaintedAgentMark): number | null {
  const ended = endedAt(painted);
  if (ended === null) return null;
  return lingers(painted.mark) ? ended + AGENT_LINGER_MS : ended;
}

export function agentMarkPhase(painted: PaintedAgentMark, now: number): AgentMarkPhase {
  const leaving = agentMarkLeavingAt(painted);
  if (leaving === null) return "live";
  if (now < leaving) return "past";
  return now - leaving < AGENT_FADE_OUT_MS ? "leaving" : "gone";
}

/** The next time any mark's phase or fade changes, or null when none will. */
export function nextAgentPaintChange(marks: readonly PaintedAgentMark[], now: number): number | null {
  let due = Number.POSITIVE_INFINITY;
  for (const painted of marks) {
    if (now - painted.bornAt < AGENT_FADE_IN_MS) due = Math.min(due, painted.bornAt + AGENT_FADE_IN_MS);
    const leaving = agentMarkLeavingAt(painted);
    if (leaving === null) continue;
    if (now < leaving) due = Math.min(due, leaving);
    else if (now < leaving + AGENT_FADE_OUT_MS) due = Math.min(due, leaving + AGENT_FADE_OUT_MS);
  }
  return Number.isFinite(due) ? due : null;
}

function stepRank(mark: AgentMark): number {
  const source = mark.source;
  return source.kind === "intent" ? (source.item.state === "awaiting_approval" ? 0 : 1)
    : source.kind === "draft" ? 2 : 3;
}

function newerStep(a: AgentMark, b: AgentMark): boolean {
  const rank = stepRank(a) - stepRank(b);
  if (rank !== 0) return rank < 0;
  return a.source.kind === "read" && b.source.kind === "read" && a.source.item.sequence > b.source.item.sequence;
}

/** Each chat's current step: an approval-held change, then any pending change or draft, then its newest read. */
function currentItems(marks: readonly AgentMark[]): Set<unknown> {
  const chosen = new Map<string, AgentMark>();
  for (const mark of marks) {
    const held = chosen.get(mark.chat.sessionId);
    if (!held || newerStep(mark, held)) chosen.set(mark.chat.sessionId, mark);
  }
  return new Set([...chosen.values()].map((mark) => mark.source.item));
}

/** Carries fade timing across updates; ended and superseded marks stay until gone. */
export function nextAgentPaint(
  previous: readonly PaintedAgentMark[],
  reported: readonly ReportedAgentMark[],
  now: number,
): PaintedAgentMark[] {
  const prior = new Map(previous.map((painted) => [painted.mark.key, painted]));
  const current = currentItems(reported.map((item) => item.mark));
  const next: PaintedAgentMark[] = [];
  const present = new Set<string>();
  for (const item of reported) {
    const held = prior.get(item.mark.key);
    present.add(item.mark.key);
    const superseded = lingers(item.mark) && !current.has(item.mark.source.item);
    next.push({
      ...item,
      bornAt: held && held.leftAt === null ? held.bornAt : now,
      leftAt: null,
      supersededAt: !superseded ? null
        : held?.leftAt === null ? held.supersededAt ?? now
        // A match already behind its chat when this document first sees it ended earlier.
        : Number.NEGATIVE_INFINITY,
    });
  }
  for (const painted of previous) {
    if (present.has(painted.mark.key)) continue;
    const departed = painted.leftAt === null ? { ...painted, leftAt: now } : painted;
    if (agentMarkPhase(departed, now) !== "gone") next.push(departed);
  }
  return next;
}

/**
 * What the text draws: each chat's live step, its newest past matches, and
 * every mark fading out. Earlier reads remain in the gutter.
 */
export function currentAgentSteps(marks: readonly PaintedAgentMark[], now: number): PaintedAgentMark[] {
  const current = currentItems(marks.filter((painted) => painted.leftAt === null && painted.supersededAt === null).map((painted) => painted.mark));
  const pastByChat = new Map<string, { painted: PaintedAgentMark; ended: number }[]>();
  const drawn = new Set<PaintedAgentMark>();
  for (const painted of marks) {
    const phase = agentMarkPhase(painted, now);
    const ended = endedAt(painted);
    if (phase === "live" && current.has(painted.mark.source.item)) drawn.add(painted);
    else if (phase === "leaving") drawn.add(painted);
    else if (phase === "past" && ended !== null) {
      const list = pastByChat.get(painted.mark.chat.sessionId) ?? [];
      list.push({ painted, ended });
      pastByChat.set(painted.mark.chat.sessionId, list);
    }
  }
  for (const list of pastByChat.values()) {
    const newest = new Map<unknown, number>();
    for (const { painted, ended } of list) {
      const item = painted.mark.source.item;
      newest.set(item, Math.max(newest.get(item) ?? ended, ended));
    }
    const kept = new Set([...newest].sort((a, b) => b[1] - a[1]).slice(0, AGENT_PAST_MARKS_MAX).map(([item]) => item));
    for (const { painted } of list) if (kept.has(painted.mark.source.item)) drawn.add(painted);
  }
  return marks.filter((painted) => drawn.has(painted));
}

/** Only reads tint the gutter column; pending changes stay in the text. */
export function agentWholeFile(marks: readonly PaintedAgentMark[]): PaintedAgentMark | null {
  let chosen: PaintedAgentMark | null = null;
  for (const painted of marks) {
    if (painted.mark.kind !== "whole") continue;
    const leaving = painted.leftAt !== null, heldLeaving = chosen?.leftAt != null;
    const source = painted.mark.source, prior = chosen?.mark.source;
    const newer = source.kind === "read" && prior?.kind === "read" && painted.mark.chat.sessionId === chosen?.mark.chat.sessionId
      ? source.item.sequence > prior.item.sequence : painted.bornAt > (chosen?.bornAt ?? 0);
    if (!chosen || (heldLeaving && !leaving) || (leaving === heldLeaving && newer)) chosen = painted;
  }
  return chosen;
}
