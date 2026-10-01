/** Session-scoped File Editor jump history. */

export type JumpEntry = {
  bufferKey: string;
  rootId: string;
  path: string;
  jobId?: string;
  line: number;
};

export type JumpHistory = {
  /** Past locations (oldest → newest); current sits at the tip after a push. */
  back: JumpEntry[];
  forward: JumpEntry[];
  /** Last coalesced push clock (ms). */
  lastPushAt: number;
  lastPush: JumpEntry | null;
};

export const JUMP_HISTORY_CAP = 100;
const JUMP_COALESCE_MS = 500;
/** Cursor moves of more than this many lines count as a teleport. */
export const JUMP_CURSOR_LINE_THRESHOLD = 10;

export function createJumpHistory(): JumpHistory {
  return { back: [], forward: [], lastPushAt: 0, lastPush: null };
}

function sameEntry(a: JumpEntry, b: JumpEntry): boolean {
  return a.bufferKey === b.bufferKey && a.line === b.line;
}

/** Push a location and coalesce rapid moves within one buffer. */
export function pushJump(
  hist: JumpHistory,
  entry: JumpEntry,
  now = Date.now(),
): JumpHistory {
  const line = Math.max(1, Math.floor(entry.line));
  const nextEntry: JumpEntry = { ...entry, line };
  if (
    hist.lastPush &&
    hist.lastPush.bufferKey === nextEntry.bufferKey &&
    now - hist.lastPushAt < JUMP_COALESCE_MS
  ) {
    // Coalesce rapid moves at the history tip.
    const back = hist.back.slice();
    if (back.length > 0) {
      back[back.length - 1] = nextEntry;
    } else {
      back.push(nextEntry);
    }
    return {
      back: trimCap(back),
      forward: [],
      lastPushAt: now,
      lastPush: nextEntry,
    };
  }
  if (hist.lastPush && sameEntry(hist.lastPush, nextEntry)) {
    return hist;
  }
  const back = [...hist.back, nextEntry];
  return {
    back: trimCap(back),
    forward: [],
    lastPushAt: now,
    lastPush: nextEntry,
  };
}

/** Move to the previous location. */
export function jumpBack(hist: JumpHistory): {
  hist: JumpHistory;
  entry: JumpEntry | null;
} {
  const current = hist.back[hist.back.length - 1];
  const target = hist.back[hist.back.length - 2];
  if (!current || !target) {
    return { hist, entry: null };
  }
  const back = hist.back.slice(0, -1);
  return {
    hist: {
      back,
      forward: [current, ...hist.forward],
      lastPushAt: hist.lastPushAt,
      lastPush: target,
    },
    entry: target,
  };
}

/** Move to the next location. */
export function jumpForward(hist: JumpHistory): {
  hist: JumpHistory;
  entry: JumpEntry | null;
} {
  const [next, ...rest] = hist.forward;
  if (!next) {
    return { hist, entry: null };
  }
  return {
    hist: {
      back: trimCap([...hist.back, next]),
      forward: rest,
      lastPushAt: hist.lastPushAt,
      lastPush: next,
    },
    entry: next,
  };
}

/** Retarget entries after a path move. */
export function retargetJumpHistory(
  hist: JumpHistory,
  mapEntry: (entry: JumpEntry) => JumpEntry | null,
): JumpHistory {
  return {
    back: hist.back.map(mapEntry).filter((e): e is JumpEntry => e != null),
    forward: hist.forward
      .map(mapEntry)
      .filter((e): e is JumpEntry => e != null),
    lastPushAt: hist.lastPushAt,
    lastPush: hist.lastPush ? mapEntry(hist.lastPush) : null,
  };
}

function trimCap(back: JumpEntry[]): JumpEntry[] {
  if (back.length <= JUMP_HISTORY_CAP) return back;
  return back.slice(back.length - JUMP_HISTORY_CAP);
}
