import { createSignal } from "solid-js";
import {
  CONTEXT_NAV_CATALOG,
  DEFAULT_CONTEXT_NAV_VISIBLE,
  type ContextNavItemId,
  type DenContextNavPrefs,
} from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const CONTEXT_NAV_ID_SET = new Set<string>(CONTEXT_NAV_CATALOG);

export function isContextNavItemId(value: string): value is ContextNavItemId {
  return CONTEXT_NAV_ID_SET.has(value);
}

/** Dedupe + drop unknown ids; preserve first-seen order. */
export function normalizeContextNavVisible(
  ids: readonly string[],
): ContextNavItemId[] {
  const out: ContextNavItemId[] = [];
  const seen = new Set<ContextNavItemId>();
  for (const raw of ids) {
    if (!isContextNavItemId(raw) || seen.has(raw)) continue;
    seen.add(raw);
    out.push(raw);
  }
  return out;
}

export function resolveContextNavVisible(
  prefs?: DenContextNavPrefs,
): ContextNavItemId[] {
  if (prefs?.visible === undefined) {
    return [...DEFAULT_CONTEXT_NAV_VISIBLE];
  }
  return normalizeContextNavVisible(prefs.visible);
}

const [visibleIds, setVisibleIds] = createSignal<ContextNavItemId[]>([
  ...DEFAULT_CONTEXT_NAV_VISIBLE,
]);

/** Reactive ordered Context pins. */
export function contextNavVisiblePref(): ContextNavItemId[] {
  return visibleIds();
}

export function syncContextNavPrefsFromSnapshot(): void {
  setVisibleIds(resolveContextNavVisible(getAppStateSnapshot().contextNav));
}

export async function saveContextNavVisible(
  ids: readonly ContextNavItemId[],
): Promise<void> {
  const visible = normalizeContextNavVisible(ids);
  setVisibleIds(visible);
  await persistAppStateInBackground({
    contextNav: { visible },
  });
}

export function reorderContextNavVisible(
  ids: readonly ContextNavItemId[],
  fromId: ContextNavItemId,
  toId: ContextNavItemId,
): ContextNavItemId[] {
  if (fromId === toId) return [...ids];
  const next = [...ids];
  const from = next.indexOf(fromId);
  const to = next.indexOf(toId);
  if (from < 0 || to < 0) return next;
  next.splice(from, 1);
  next.splice(to, 0, fromId);
  return next;
}
