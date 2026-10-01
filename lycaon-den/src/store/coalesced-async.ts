export type CoalescedAsyncScheduler = {
  schedule: () => void;
  cancel: () => void;
};

/** Cooldown as a multiple of run time; sustained churn runs at most a quarter of the time. */
const RUN_DUTY_FACTOR = 3;

/** Cooldown ceiling, as a multiple of the scheduler's maximum wait. */
const RUN_COOLDOWN_CAP_FACTOR = 4;

/** Wait after a run of `durationMs`; a slow backend gets proportionally longer gaps. */
export function runCooldownMs(durationMs: number, capMs: number): number {
  return Math.min(RUN_DUTY_FACTOR * Math.max(0, durationMs), Math.max(0, capMs));
}

/**
 * Runs once a burst goes quiet, or after `maxWaitMs` if it never does. One run is
 * in flight at a time; work arriving during it starts a new burst afterwards.
 */
export function createBoundedDebouncedAsyncScheduler(
  run: () => Promise<void>,
  idleMs: number,
  maxWaitMs: number,
): CoalescedAsyncScheduler {
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let maxTimer: ReturnType<typeof setTimeout> | undefined;
  let running = false;
  let pending = false;
  let cooldownUntil = 0;

  const clearTimers = () => {
    if (idleTimer !== undefined) clearTimeout(idleTimer);
    if (maxTimer !== undefined) clearTimeout(maxTimer);
    idleTimer = undefined;
    maxTimer = undefined;
  };

  const flush = async () => {
    clearTimers();
    if (!pending) return;
    if (running) return;
    pending = false;
    running = true;
    const startedAt = Date.now();
    try {
      await run();
    } finally {
      running = false;
      const settledAt = Date.now();
      cooldownUntil =
        settledAt +
        runCooldownMs(
          settledAt - startedAt,
          RUN_COOLDOWN_CAP_FACTOR * maxWaitMs,
        );
      if (pending) arm();
    }
  };

  const arm = () => {
    if (running) return;
    const cooldown = Math.max(0, cooldownUntil - Date.now());
    if (idleTimer !== undefined) clearTimeout(idleTimer);
    idleTimer = setTimeout(() => void flush(), Math.max(cooldown, idleMs, 0));
    maxTimer ??= setTimeout(() => void flush(), Math.max(cooldown, maxWaitMs, 0));
  };

  return {
    schedule() {
      pending = true;
      arm();
    },
    cancel() {
      clearTimers();
      pending = false;
    },
  };
}

/** Runs at most once per interval; a getter interval is re-read on every schedule. */
export function createThrottledAsyncScheduler(
  run: () => Promise<void>,
  intervalMs: number | (() => number),
): CoalescedAsyncScheduler {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let running = false;
  let pending = false;
  let lastRunAt = 0;

  const interval = () =>
    typeof intervalMs === "function" ? intervalMs() : intervalMs;

  // The armed timer is the cooldown marker, whether or not a run is pending.
  const armTrailing = () => {
    const wait = Math.max(0, lastRunAt + interval() - Date.now());
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      if (!pending) return;
      pending = false;
      void flush();
    }, wait);
  };

  const flush = async () => {
    if (running) {
      pending = true;
      return;
    }
    running = true;
    try {
      await run();
    } finally {
      running = false;
      lastRunAt = Date.now();
      armTrailing();
    }
  };

  return {
    schedule() {
      pending = true;
      if (running) return;
      if (timer !== undefined) {
        armTrailing();
        return;
      }
      pending = false;
      void flush();
    },
    cancel() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      pending = false;
    },
  };
}

/** Debounces bursts; a call during a run schedules one trailing rerun. */
export function createCoalescedAsyncScheduler(
  run: () => Promise<void>,
  delayMs: number,
): CoalescedAsyncScheduler {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let running = false;
  let rerun = false;

  const flush = async () => {
    timer = undefined;
    if (running) {
      rerun = true;
      return;
    }
    running = true;
    try {
      await run();
    } finally {
      running = false;
      if (rerun) {
        rerun = false;
        schedule();
      }
    }
  };

  const schedule = () => {
    if (timer !== undefined) return;
    timer = setTimeout(() => {
      void flush();
    }, delayMs);
  };

  return {
    schedule,
    cancel() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      rerun = false;
    },
  };
}
