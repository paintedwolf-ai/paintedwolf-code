import { createSignal } from "solid-js";
import { parseInstant } from "../time/time-copy.ts";

/**
 * The seen stamp a chat's "New" line compares rows against, captured when the
 * chat becomes readable and held for the visit. Readable requires window
 * focus, so a stamp younger than the absence threshold draws no line.
 */
export const UNREAD_ABSENCE_MS = 5 * 60_000;

const [boundaries, setBoundaries] = createSignal<Readonly<Record<string, number>>>({});

function withoutSession(prev: Readonly<Record<string, number>>, id: string) {
  if (!(id in prev)) return prev;
  const next = { ...prev };
  delete next[id];
  return next;
}

/** Epoch ms of the stamp rows are compared against; null when there is no line to draw. */
export function unreadSince(sessionId: string | null | undefined): number | null {
  const id = sessionId?.trim();
  if (!id) return null;
  return boundaries()[id] ?? null;
}

/** Records the previous seen stamp; a chat never read, or read recently, has no line. */
export function captureUnreadBoundary(
  sessionId: string,
  seenAt: string | null | undefined,
  now: number,
): void {
  const id = sessionId.trim();
  if (!id) return;
  const at = parseInstant(seenAt);
  setBoundaries((prev) => {
    if (at === null || now - at < UNREAD_ABSENCE_MS) return withoutSession(prev, id);
    return prev[id] === at ? prev : { ...prev, [id]: at };
  });
}

/** Removes the line, such as when the person sends a prompt. */
export function clearUnreadBoundary(sessionId: string | null | undefined): void {
  const id = sessionId?.trim();
  if (!id) return;
  setBoundaries((prev) => withoutSession(prev, id));
}

/** Reset module state between test cases. */
export function resetUnreadBoundariesForTests(): void {
  setBoundaries({});
}
