import type { FileBufferKey } from "../components/project-files-model.ts";

type StripBufferFacts = {
  pinned: boolean;
  dirty: boolean;
};

export type CloseFamily =
  | "close"
  | "closeOthers"
  | "closeToTheRight"
  | "closeSaved"
  | "closeAll";

/** Pinned tabs lead; each group preserves its order. */
export function orderWithPinnedGroup(
  order: readonly FileBufferKey[],
  byKey: Readonly<Record<string, Pick<StripBufferFacts, "pinned">>>,
): FileBufferKey[] {
  const pinned: FileBufferKey[] = [];
  const rest: FileBufferKey[] = [];
  for (const key of order) {
    if (byKey[key]?.pinned) pinned.push(key);
    else rest.push(key);
  }
  return [...pinned, ...rest];
}

/** Dragging stays within the tab’s pinned or unpinned group. */
export function clampDragIndexWithinGroup(
  order: readonly FileBufferKey[],
  byKey: Readonly<Record<string, Pick<StripBufferFacts, "pinned">>>,
  fromIndex: number,
  toIndex: number,
): number {
  if (fromIndex < 0 || fromIndex >= order.length) return fromIndex;
  const key = order[fromIndex]!;
  const pinned = Boolean(byKey[key]?.pinned);
  let lo = 0;
  let hi = order.length - 1;
  for (let i = 0; i < order.length; i++) {
    if (Boolean(byKey[order[i]!]?.pinned) === pinned) {
      lo = i;
      break;
    }
  }
  for (let i = order.length - 1; i >= 0; i--) {
    if (Boolean(byKey[order[i]!]?.pinned) === pinned) {
      hi = i;
      break;
    }
  }
  return Math.max(lo, Math.min(hi, toIndex));
}

/** Only explicit close and close-all actions include pinned tabs. */
export function closeTargetKeys(
  family: CloseFamily,
  order: readonly FileBufferKey[],
  byKey: Readonly<Record<string, Pick<StripBufferFacts, "pinned" | "dirty">>>,
  focusKey: FileBufferKey | null,
): FileBufferKey[] {
  switch (family) {
    case "close":
      return focusKey && byKey[focusKey] ? [focusKey] : [];
    case "closeOthers": {
      if (!focusKey) return [];
      return order.filter((k) => k !== focusKey && !byKey[k]?.pinned);
    }
    case "closeToTheRight": {
      if (!focusKey) return [];
      const idx = order.indexOf(focusKey);
      if (idx < 0) return [];
      return order.slice(idx + 1).filter((k) => !byKey[k]?.pinned);
    }
    case "closeSaved":
      return order.filter((k) => !byKey[k]?.dirty && !byKey[k]?.pinned);
    case "closeAll":
      return [...order];
  }
}

export function multiFileDirtyDialogBody(
  names: readonly string[],
  cap = 8,
): { listed: string[]; andMore: number } {
  if (names.length <= cap) return { listed: [...names], andMore: 0 };
  return { listed: names.slice(0, cap), andMore: names.length - cap };
}

/** The suffix stays visible while the leading text clips. */
export function splitFileNameForMiddleEllipsis(name: string): {
  start: string;
  end: string;
} {
  const dot = name.lastIndexOf(".");
  if (dot > 0 && name.length - dot <= 8) {
    return { start: name.slice(0, dot), end: name.slice(dot) };
  }
  if (name.length <= 12) return { start: name, end: "" };
  return { start: name.slice(0, -6), end: name.slice(-6) };
}
