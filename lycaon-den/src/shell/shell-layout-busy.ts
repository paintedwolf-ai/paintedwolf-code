import { perfMark } from "../chat/stream/den-main-thread-perf.ts";

const BUSY_CLASS = "den-shell--layout-busy";

/** Quiet interval that ends a continuous resize. */
const SUSTAIN_QUIET_MS = 120;

let busyDepth = 0;
const settleListeners = new Set<() => void>();

/** Whether continuous resize controls a busy token. */
let sustaining = false;
let sustainTimer: ReturnType<typeof setTimeout> | undefined;
let settleFrame: number | undefined;
let settleChildFrame: number | undefined;
let settlePending = false;

function cancelSettledNotify(): void {
  if (settleFrame !== undefined) {
    cancelAnimationFrame(settleFrame);
    settleFrame = undefined;
  }
  if (settleChildFrame !== undefined) {
    cancelAnimationFrame(settleChildFrame);
    settleChildFrame = undefined;
  }
}

function notifySettled(): void {
  for (const listener of settleListeners) listener();
}

function finishSettle(): void {
  if (!settlePending) return;
  settlePending = false;
  notifySettled();
}

/** Notify after two frames so listeners measure the live box. */
function releaseBusy(): void {
  cancelSettledNotify();
  settlePending = true;
  if (typeof document !== "undefined") {
    document.documentElement.classList.remove(BUSY_CLASS);
  }
  perfMark("layout:busy:settle");
  if (typeof requestAnimationFrame !== "function") {
    finishSettle();
    return;
  }
  settleFrame = requestAnimationFrame(() => {
    settleFrame = undefined;
    settleChildFrame = requestAnimationFrame(() => {
      settleChildFrame = undefined;
      finishSettle();
    });
  });
}

function enterBusy(): void {
  if (typeof document !== "undefined") {
    document.documentElement.classList.add(BUSY_CLASS);
  }
  perfMark("layout:busy:begin");
}

export function isShellLayoutBusy(): boolean {
  return busyDepth > 0;
}

/** Layout observers stay gated through the two-frame measurement settle. */
export function isShellLayoutUnstable(): boolean {
  return busyDepth > 0 || settlePending;
}

export function beginShellLayoutBusy(): void {
  if (busyDepth === 0) {
    cancelSettledNotify();
    settlePending = false;
    enterBusy();
  }
  busyDepth++;
}

export function endShellLayoutBusy(): void {
  if (busyDepth <= 0) return;
  busyDepth--;
  if (busyDepth === 0) releaseBusy();
}

/** Keep reactive and asynchronous geometry writes in one layout epoch. */
export async function runShellLayoutTransaction<T>(
  change: () => T | Promise<T>,
): Promise<T> {
  beginShellLayoutBusy();
  try {
    return await change();
  } finally {
    endShellLayoutBusy();
  }
}

/** When the quiet window currently expires; each event moves it, not the timer. */
let sustainDeadline = 0;

/** Extends busy state until a continuous resize becomes quiet. */
export function sustainShellLayoutBusy(): void {
  if (!sustaining) {
    sustaining = true;
    beginShellLayoutBusy();
  }
  // Continuous resize updates the deadline instead of re-arming the timer.
  sustainDeadline = Date.now() + SUSTAIN_QUIET_MS;
  if (sustainTimer !== undefined) return;
  const settleWhenQuiet = () => {
    // Cap remaining delay against backward clock adjustments.
    const remaining = Math.min(sustainDeadline - Date.now(), SUSTAIN_QUIET_MS);
    if (remaining > 0) {
      sustainTimer = setTimeout(settleWhenQuiet, remaining);
      return;
    }
    sustainTimer = undefined;
    sustaining = false;
    endShellLayoutBusy();
  };
  sustainTimer = setTimeout(settleWhenQuiet, SUSTAIN_QUIET_MS);
}

export function onShellLayoutSettled(listener: () => void): () => void {
  settleListeners.add(listener);
  return () => {
    settleListeners.delete(listener);
  };
}


/** Reset busy state between tests. */
export function resetShellLayoutBusyForTests(): void {
  busyDepth = 0;
  sustaining = false;
  sustainDeadline = 0;
  if (sustainTimer !== undefined) {
    clearTimeout(sustainTimer);
    sustainTimer = undefined;
  }
  cancelSettledNotify();
  settlePending = false;
  if (typeof document !== "undefined") {
    document.documentElement.classList.remove(BUSY_CLASS);
  }
}

/** Drain a pending settle without waiting on animation frames. */
export async function flushShellLayoutSettleForTests(): Promise<void> {
  cancelSettledNotify();
  finishSettle();
}
