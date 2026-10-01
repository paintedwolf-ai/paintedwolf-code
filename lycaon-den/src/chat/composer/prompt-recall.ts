import { createSignal } from "solid-js";

/** How many sent prompts a session remembers for ↑ recall. */
export const PROMPT_RECALL_CAP = 20;

/** Per-session prompt recall lasts for the current window. */
const [rings, setRings] = createSignal<Record<string, string[]>>({});
/** Per-session cursor: -1 means "not in recall", 0 is the newest prompt. */
const [cursors, setCursors] = createSignal<Record<string, number>>({});

function ringFor(sessionId: string): string[] {
  return rings()[sessionId] ?? [];
}

export function recordSentPrompt(
  sessionId: string | null | undefined,
  text: string,
): void {
  const id = sessionId?.trim();
  const value = text.trim();
  if (!id || !value) return;
  setRings((prev) => {
    const current = prev[id] ?? [];
    // Consecutive repeats share one history entry.
    const deduped = current[0] === value ? current : [value, ...current];
    return { ...prev, [id]: deduped.slice(0, PROMPT_RECALL_CAP) };
  });
  setCursors((prev) => ({ ...prev, [id]: -1 }));
}

/** Returns null at the history boundary and an empty string when leaving recall. */
export function walkRecall(
  sessionId: string | null | undefined,
  direction: "older" | "newer",
): string | null {
  const id = sessionId?.trim();
  if (!id) return null;
  const ring = ringFor(id);
  if (ring.length === 0) return null;
  const cursor = cursors()[id] ?? -1;
  const next = direction === "older" ? cursor + 1 : cursor - 1;
  if (next >= ring.length) return null; // already at the oldest
  if (next < -1) return null; // already out of recall
  setCursors((prev) => ({ ...prev, [id]: next }));
  return next < 0 ? "" : (ring[next] ?? "");
}

/** An unselected caret recalls from the first line upward or the last line downward. */
export function caretAtRecallEdge(
  value: string,
  selectionStart: number,
  selectionEnd: number,
  direction: "older" | "newer",
): boolean {
  if (selectionStart !== selectionEnd) return false;
  return direction === "older"
    ? !value.slice(0, selectionStart).includes("\n")
    : !value.slice(selectionEnd).includes("\n");
}

/** True while the session is walking its ring rather than composing fresh text. */
export function inRecall(sessionId: string | null | undefined): boolean {
  const id = sessionId?.trim();
  if (!id) return false;
  return (cursors()[id] ?? -1) >= 0;
}

/** Typing leaves recall while preserving the edited text. */
export function exitRecall(sessionId: string | null | undefined): void {
  const id = sessionId?.trim();
  if (!id) return;
  setCursors((prev) => (prev[id] === undefined || prev[id] === -1 ? prev : { ...prev, [id]: -1 }));
}

export function resetPromptRecallForTests(): void {
  setRings({});
  setCursors({});
}
