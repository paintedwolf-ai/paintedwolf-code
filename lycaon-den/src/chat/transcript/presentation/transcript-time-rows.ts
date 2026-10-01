import type { PendingSend } from "../../send/pending-sends.ts";
import type { Message, TurnClock } from "../../../api/types.ts";
import { parseInstant } from "../../../time/time-copy.ts";
import { activitySpanToolParts } from "../../tool/activity-span-model.ts";
import type { DisplayTranscriptItem, TranscriptTimeItem } from "../projection/transcript-item-model.ts";
import { transcriptRowOpensTurn } from "../projection/transcript-turns.ts";

/** Same-day idle interval that gives the next prompt a timestamp. */
const TRANSCRIPT_QUIET_GAP_MS = 60 * 60_000;

export type TranscriptTimeRowContext = {
  messageById: ReadonlyMap<string, Message>;
  /** Pending rows follow resident host rows and use the same calendar rules. */
  pendingSends?: readonly PendingSend[];
  /** Visible user turn clocks keyed by opening message id. */
  turnClocks: Readonly<Record<string, TurnClock>>;
  /** The person's seen stamp when this chat last became readable. */
  unreadSince?: number | null;
  /** True when earlier messages in history exist before these items. */
  hasMoreBefore?: boolean;
  /** The session still holds live work, so its newest clock is paused, not finished. */
  sessionLive?: boolean;
  /** Calendar day of an instant in the viewer's zone. */
  dayKey: (at: number) => number;
};

type RowAnchor = { message: Message; at: number | null };

/** Every message that places a row; its first by `ord` dates the row. */
function rowMessageIds(item: DisplayTranscriptItem): string[] {
  switch (item.kind) {
    case "tool":
      return [item.part.messageId];
    case "checkpoint":
      return [item.parentMessageId];
    case "activity_span":
      return activitySpanToolParts(item.entries).map((part) => part.messageId);
    case "worker_group":
      return item.parts.map((part) => part.messageId);
    case "file_edit":
    case "worker_file_edit":
    case "time_marker":
    case "unread_marker":
    case "turn_tail":
      return [item.anchorMessageId];
    default:
      return [item.key];
  }
}

function rowAnchor(
  item: DisplayTranscriptItem,
  messageById: ReadonlyMap<string, Message>,
): RowAnchor | null {
  let first: Message | undefined;
  for (const id of rowMessageIds(item)) {
    const message = messageById.get(id);
    if (message && (!first || (message.ord ?? 0) < (first.ord ?? 0))) first = message;
  }
  return first ? { message: first, at: parseInstant(first.created_at) } : null;
}

function lastRowMessageId(
  item: DisplayTranscriptItem,
  messageById: ReadonlyMap<string, Message>,
): string | null {
  let last: Message | undefined;
  for (const id of rowMessageIds(item)) {
    const message = messageById.get(id);
    if (message && (!last || (message.ord ?? 0) >= (last.ord ?? 0))) last = message;
  }
  return last?.id ?? null;
}

/** Turn geometry follows the same prompt boundaries as the seams, before clocks arrive. */
function tailPlacements(
  items: readonly DisplayTranscriptItem[],
  ctx: TranscriptTimeRowContext,
): Map<number, Extract<TranscriptTimeItem, { kind: "turn_tail" }>> {
  const placements = new Map<number, Extract<TranscriptTimeItem, { kind: "turn_tail" }>>();
  let opening: DisplayTranscriptItem | undefined;
  let last = -1;
  const continuation = (key: string) => ctx.messageById.get(key)?.kind === "user_continuation";
  const place = (newest: boolean) => {
    if (!opening || last < 0) return;
    const clock = ctx.turnClocks[opening.key];
    const anchor = items[last]!;
    const anchorMessageId = lastRowMessageId(anchor, ctx.messageById) ??
      (anchor.kind === "pending_user" ? anchor.key : opening.key);
    placements.set(last, {
      kind: "turn_tail",
      key: `turn-tail:${opening.key}`,
      openingMessageId: opening.key,
      anchorMessageId,
      startedAt: opening.kind === "pending_user" ? opening.pending.createdAt :
        parseInstant(ctx.messageById.get(opening.key)?.created_at),
      settledAt: !clock || clock.running || (newest && ctx.sessionLive)
        ? null : parseInstant(clock.settled_at),
      activeMs: clock?.active_ms ?? 0,
      workMs: clock?.work_ms ?? 0,
    });
  };
  for (const [index, item] of items.entries()) {
    if (transcriptRowOpensTurn(item, continuation)) {
      place(false);
      opening = item;
    }
    last = index;
  }
  place(true);
  return placements;
}

/** Timestamps can decrease in ordinal order; day labels only advance. */
export function withTranscriptTimeRows(
  items: readonly DisplayTranscriptItem[],
  ctx: TranscriptTimeRowContext,
): DisplayTranscriptItem[] {
  const unreadSince = ctx.unreadSince ?? null;
  const out: DisplayTranscriptItem[] = [];
  let day: number | null = null;
  let latest: number | null = null;
  let unreadPlaced = unreadSince === null;
  let seenBoundary = !ctx.hasMoreBefore;
  const seenDayBoundary = !ctx.hasMoreBefore;

  // Host echoes can precede pending-store cleanup.
  const pending: DisplayTranscriptItem[] = (ctx.pendingSends ?? [])
    .filter((entry) => entry.kind !== "queued_prompt" && !ctx.messageById.has(entry.operationId))
    .map((entry) => ({ kind: "pending_user", key: entry.operationId, text: entry.text, pending: entry }));
  const displayed = [...items, ...pending];
  const anchors = displayed.map((item) => rowAnchor(item, ctx.messageById));
  const tails = tailPlacements(displayed, ctx);
  for (const [index, item] of displayed.entries()) {
    const anchor = anchors[index];
    const at = anchor?.at ?? (item.kind === "pending_user" ? item.pending.createdAt : null);
    const prompt = item.kind === "user" || item.kind === "pending_user";
    if (at !== null && Number.isFinite(at)) {
      const rowDay = ctx.dayKey(at);
      const anchorMessageId = anchor?.message.id ?? item.key;
      if (day === null) {
        day = rowDay;
        if (seenDayBoundary) {
          out.push({ kind: "time_marker", key: `time:day:${item.key}`, variant: "day", at, anchorMessageId });
        }
      } else if (rowDay > day) {
        day = rowDay;
        out.push({ kind: "time_marker", key: `time:day:${item.key}`, variant: "day", at, anchorMessageId });
      } else if (prompt && latest !== null && at - latest >= TRANSCRIPT_QUIET_GAP_MS) {
        out.push({ kind: "time_marker", key: `time:gap:${item.key}`, variant: "gap", at, anchorMessageId });
      }
      if (unreadSince !== null && at <= unreadSince) {
        seenBoundary = true;
      }
      if (!unreadPlaced && seenBoundary && item.kind !== "pending_user" && at > unreadSince!) {
        unreadPlaced = true;
        out.push({ kind: "unread_marker", key: "unread", seenAt: unreadSince!, anchorMessageId });
      }
      latest = latest === null ? at : Math.max(latest, at);
    }
    out.push(item);
    const tail = tails.get(index);
    if (tail) {
      out.push(tail);
      if (tail.settledAt !== null) {
        latest = latest === null ? tail.settledAt : Math.max(latest, tail.settledAt);
      }
    }
  }
  return out;
}

export function isTranscriptTimeItem(
  item: DisplayTranscriptItem,
): item is Extract<DisplayTranscriptItem, TranscriptTimeItem> {
  return item.kind === "time_marker" || item.kind === "unread_marker" || item.kind === "turn_tail";
}
