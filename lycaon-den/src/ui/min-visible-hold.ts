/** Minimum readable interval for transient state. */
export const MIN_VISIBLE_MS = 1_600;

type TimeoutHandle = ReturnType<typeof setTimeout>;

export type MinVisibleHoldOptions<V> = {
  /** Values that describe the same state keep their first-shown time. */
  same?: (shown: V, next: V) => boolean;
  minVisibleMs?: number;
  now?: () => number;
  schedule?: (callback: () => void, delayMs: number) => TimeoutHandle;
  clearSchedule?: (handle: TimeoutHandle) => void;
};

type Visible<V> = {
  value: V;
  shownAt: number;
};

/** Updates immediately and holds removals for the minimum display interval. */
export class MinVisibleHold<V> {
  private readonly visible = new Map<string, Visible<V>>();
  private active: ReadonlyMap<string, V> = new Map();
  private timer: TimeoutHandle | null = null;
  private disposed = false;
  private readonly same: (shown: V, next: V) => boolean;
  private readonly minVisibleMs: number;
  private readonly now: () => number;
  private readonly schedule: (callback: () => void, delayMs: number) => TimeoutHandle;
  private readonly clearSchedule: (handle: TimeoutHandle) => void;

  constructor(
    private readonly onChange: (visible: ReadonlyMap<string, V>) => void,
    options: MinVisibleHoldOptions<V> = {},
  ) {
    this.same = options.same ?? Object.is;
    this.minVisibleMs = options.minVisibleMs ?? MIN_VISIBLE_MS;
    this.now = options.now ?? Date.now;
    this.schedule = options.schedule ?? ((callback, delayMs) => setTimeout(callback, delayMs));
    this.clearSchedule = options.clearSchedule ?? ((handle) => clearTimeout(handle));
  }

  update(active: ReadonlyMap<string, V>): void {
    if (this.disposed) return;
    this.active = active;
    const at = this.now();
    let changed = false;

    for (const [key, value] of active) {
      const current = this.visible.get(key);
      if (current && this.same(current.value, value)) {
        current.value = value;
        continue;
      }
      this.visible.set(key, { value, shownAt: at });
      changed = true;
    }

    changed = this.expireEligible(at) || changed;
    this.reschedule(at);
    if (changed) this.publish();
  }

  /** Drops everything held, as when the presented scope changes. */
  clear(): void {
    if (this.disposed) return;
    this.clearTimer();
    this.active = new Map();
    if (this.visible.size === 0) return;
    this.visible.clear();
    this.publish();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.clearTimer();
    this.active = new Map();
    this.visible.clear();
  }

  private expireEligible(at: number): boolean {
    let changed = false;
    for (const [key, entry] of this.visible) {
      if (this.active.has(key)) continue;
      if (at - entry.shownAt < this.minVisibleMs) continue;
      this.visible.delete(key);
      changed = true;
    }
    return changed;
  }

  private reschedule(at: number): void {
    this.clearTimer();
    let nextDelay = Number.POSITIVE_INFINITY;
    for (const [key, entry] of this.visible) {
      if (this.active.has(key)) continue;
      nextDelay = Math.min(nextDelay, this.minVisibleMs - (at - entry.shownAt));
    }
    if (!Number.isFinite(nextDelay)) return;
    this.timer = this.schedule(() => {
      this.timer = null;
      if (this.disposed) return;
      const current = this.now();
      if (this.expireEligible(current)) this.publish();
      this.reschedule(current);
    }, Math.max(0, nextDelay));
  }

  private clearTimer(): void {
    if (this.timer === null) return;
    this.clearSchedule(this.timer);
    this.timer = null;
  }

  private publish(): void {
    this.onChange(new Map([...this.visible].map(([key, entry]) => [key, entry.value])));
  }
}
