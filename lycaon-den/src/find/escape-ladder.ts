/** Escape priority for editor layers. */

// ORDER defines priority and valid layer ids.
const ORDER = [
  "inlineEdit",
  "findBar",
  "revealHighlight",
  "stageLatch",
] as const;

export type EscapeLadderLayerId = (typeof ORDER)[number];

export type EscapeLadderSlot = {
  isOpen: () => boolean;
  /** Dismiss or consume the layer. */
  dismiss: () => void;
};

const slots: Partial<Record<EscapeLadderLayerId, EscapeLadderSlot>> = {};

/** Layers open when the current Escape press began; one that closes itself on that press still consumes it. */
let openAtPress = new Set<EscapeLadderLayerId>();
let observingPresses = false;

function notePress(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  openAtPress = new Set(ORDER.filter((id) => slots[id]?.isOpen()));
  setTimeout(() => { openAtPress = new Set(); }, 0);
}

function observePresses(): void {
  if (observingPresses || typeof window === "undefined") return;
  observingPresses = true;
  // Observation only: capture sees the press before any layer handles it.
  window.addEventListener("keydown", notePress, true);
}

/** Register or replace a layer slot; the returned disposer clears it. */
export function registerEscapeLadderLayer(
  id: EscapeLadderLayerId,
  slot: EscapeLadderSlot,
): () => void {
  observePresses();
  slots[id] = slot;
  return () => {
    if (slots[id] === slot) delete slots[id];
  };
}

/** Dismiss the first open layer. */
export function tryConsumeEscapeLadder(
  only?: readonly EscapeLadderLayerId[],
): boolean {
  const walk = only ?? ORDER;
  for (const id of walk) {
    const slot = slots[id];
    if (!slot) continue;
    if (!slot.isOpen() && !openAtPress.has(id)) continue;
    slot.dismiss();
    return true;
  }
  return false;
}

/** Reset layer state for tests. */
export function resetEscapeLadderForTests(): void {
  for (const id of ORDER) delete slots[id];
  openAtPress = new Set();
}
