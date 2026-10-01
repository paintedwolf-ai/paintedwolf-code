/** Claimable focus targets with identity-safe replacement. */
import { createSignal } from "solid-js";
import { createClaimable, type Claimable } from "../platform/interaction/claimable.ts";

export const FOCUS_REGION_IDS = [
  "sidebar",
  "chat",
  "context",
  "composer",
  "filesTree",
  "filesTabs",
  "files",
  "find",
  "settings",
] as const;

export type FocusRegionId = (typeof FOCUS_REGION_IDS)[number];

function emptyRegions(): Record<FocusRegionId, Claimable<HTMLElement>> {
  return {
    sidebar: createClaimable(),
    chat: createClaimable(),
    context: createClaimable(),
    composer: createClaimable(),
    files: createClaimable(),
    filesTree: createClaimable(),
    filesTabs: createClaimable(),
    find: createClaimable(),
    settings: createClaimable(),
  };
}

let regions = emptyRegions();
const [focusRegionsVersion, setFocusRegionsVersion] = createSignal(0);

function bumpFocusRegions(): void {
  setFocusRegionsVersion((n) => n + 1);
}

/** Reactive focus-claim version. */
export { focusRegionsVersion };

export function registerFocusRegion(
  id: FocusRegionId,
  el: HTMLElement,
  claimToken: object,
): void {
  regions[id].claim(el, claimToken);
  bumpFocusRegions();
}

/** Release only when `claimToken` still holds the claim. */
export function releaseFocusRegion(id: FocusRegionId, claimToken: object): void {
  regions[id].release(claimToken);
  bumpFocusRegions();
}

/** True when a mounted component currently claims a visible region. */
export function isFocusRegionMounted(id: FocusRegionId): boolean {
  const el = regions[id].get();
  return el != null && !residentInactive(el);
}

function residentInactive(el: HTMLElement): boolean {
  return !el.isConnected || el.closest('[inert], [hidden], [aria-hidden="true"], [data-resident]:not([data-resident="active"])') != null;
}

/** Snapshot of mounted region ids (for command availability). */
export function mountedFocusRegions(): FocusRegionId[] {
  return FOCUS_REGION_IDS.filter((id) => isFocusRegionMounted(id));
}

/** The region container `focusRegion` is focusing, during its synchronous focus event. */
let enteringRegion: HTMLElement | null = null;

/** Focuses a claimed visible region. */
export function focusRegion(id: FocusRegionId): boolean {
  const el = regions[id].get();
  if (!el || residentInactive(el)) return false;
  const outer = enteringRegion;
  enteringRegion = el;
  try {
    el.focus({ preventScroll: true });
  } finally {
    enteringRegion = outer;
  }
  return true;
}

/**
 * True while `focusRegion` is focusing `el`. A container forwards only region entry
 * inward; pointer focus stays where the pointer put it.
 */
export function isRegionEntry(el: HTMLElement): boolean {
  return enteringRegion === el;
}

/** First focusable match under `root`, trying each selector in priority order. */
export function regionEntryTarget(
  root: HTMLElement,
  priority: readonly string[],
): HTMLElement | null {
  for (const selector of priority) {
    for (const el of root.querySelectorAll<HTMLElement>(selector)) {
      if (residentInactive(el) || el.matches(":disabled")) continue;
      return el;
    }
  }
  return null;
}

export function focusRegionContainsActive(id: FocusRegionId): boolean {
  const active = document.activeElement;
  if (!(active instanceof Node)) return false;
  const el = regions[id].get();
  return el != null && !residentInactive(el) && (active === el || el.contains(active));
}

export function activeElementInFocusRegion(
  except?: FocusRegionId | readonly FocusRegionId[],
): boolean {
  const active = document.activeElement;
  if (!(active instanceof Node)) return false;
  const skip = new Set(
    except == null ? [] : Array.isArray(except) ? except : [except],
  );
  for (const id of FOCUS_REGION_IDS) {
    if (skip.has(id)) continue;
    if (focusRegionContainsActive(id)) return true;
  }
  return false;
}

export function resetFocusRegionsForTests(): void {
  regions = emptyRegions();
  setFocusRegionsVersion((n) => n + 1);
}

/** Cycle concrete regions, omitting their enclosing catch-all containers. */
export function cycleFocusRegion(direction: 1 | -1): boolean {
  const mounted = FOCUS_REGION_IDS.filter(isFocusRegionMounted);
  const leaves = mounted.filter((id) => {
    const el = regions[id].get();
    if (!el) return false;
    return !mounted.some((other) => {
      if (other === id) return false;
      const otherEl = regions[other].get();
      return otherEl ? el.contains(otherEl) : false;
    });
  });
  if (!leaves.length) return false;
  const index = leaves.findIndex(focusRegionContainsActive);
  const next =
    index < 0
      ? direction === 1
        ? 0
        : leaves.length - 1
      : (index + direction + leaves.length) % leaves.length;
  const target = leaves[next];
  return target ? focusRegion(target) : false;
}
