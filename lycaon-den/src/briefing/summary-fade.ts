import { createMemo, type Accessor } from "solid-js";

/** Den's entry fade; reduced motion collapses it globally. */
export const SUMMARY_FADE_CLASS = "den-enter-fade";

const FADE_LIMIT = 256;

/** Summaries and sections already shown, so remounts and reopens skip the fade. */
const shown = new Set<string>();

function normalize(key: string | undefined): string {
  return key?.trim() ?? "";
}

function remember(key: string): void {
  if (shown.size >= FADE_LIMIT) shown.clear();
  shown.add(key);
}

/** Claims the first reveal of summary text. */
export function claimSummaryFade(key: string | undefined): boolean {
  const normalized = normalize(key);
  if (!normalized || shown.has(normalized)) return false;
  remember(normalized);
  return true;
}

/** Records text already available when the view opens. */
export function markSummaryShown(key: string | undefined): void {
  const normalized = normalize(key);
  if (normalized) remember(normalized);
}

export function clearSummaryFades(): void {
  shown.clear();
}

/** A completed summary fades as one group, once, only if it arrives while open. */
export function createSummaryFade(key: Accessor<string | undefined>): Accessor<boolean> {
  const identity = createMemo(key);
  markSummaryShown(identity());
  return createMemo(() => claimSummaryFade(identity()));
}
