type TimeoutHandle = ReturnType<typeof setTimeout>;

/** `live` while its fact is reported; `past` once it has ended. */
export type LingerPhase = "live" | "past";

export type Lingered<V> = {
  value: V;
  phase: LingerPhase;
  /** When the fact ended; null while live. */
  endedAt: number | null;
};

export type LingerOptions<V> = {
  /** How long a value stays after its fact ends. */
  lingerMs: number;
  /** A live value equal to the shown one publishes nothing. */
  same?: (shown: V, next: V) => boolean;
  /** False removes the value as soon as its fact ends. */
  lingers?: (value: V) => boolean;
  now?: () => number;
  schedule?: (callback: () => void, delayMs: number) => TimeoutHandle;
  clearSchedule?: (handle: TimeoutHandle) => void;
};

/**
 * Shows each reported value at once and keeps it for a fixed time after its
 * fact ends, marked past. A value reported again returns to live in place, so
 * nothing queues and nothing is shown later than `lingerMs` after it ended.
 */
export class Linger<V> {
  private shown = new Map<string, Lingered<V>>();
  private timer: TimeoutHandle | null = null;
  private disposed = false;
  private readonly lingerMs: number;
  private readonly same: (shown: V, next: V) => boolean;
  private readonly lingers: (value: V) => boolean;
  private readonly now: () => number;
  private readonly schedule: (callback: () => void, delayMs: number) => TimeoutHandle;
  private readonly clearSchedule: (handle: TimeoutHandle) => void;

  constructor(
    private readonly onChange: (shown: ReadonlyMap<string, Lingered<V>>) => void,
    options: LingerOptions<V>,
  ) {
    this.lingerMs = options.lingerMs;
    this.same = options.same ?? Object.is;
    this.lingers = options.lingers ?? (() => true);
    this.now = options.now ?? Date.now;
    this.schedule = options.schedule ?? ((callback, delayMs) => setTimeout(callback, delayMs));
    this.clearSchedule = options.clearSchedule ?? ((handle) => clearTimeout(handle));
  }

  update(active: ReadonlyMap<string, V>): void {
    if (this.disposed) return;
    const at = this.now();
    let changed = false;
    for (const [key, value] of active) {
      const current = this.shown.get(key);
      if (current?.phase === "live" && this.same(current.value, value)) {
        // Keeps the newest value for the next publish without announcing it.
        this.shown.set(key, { ...current, value });
        continue;
      }
      this.shown.set(key, { value, phase: "live", endedAt: null });
      changed = true;
    }
    for (const [key, entry] of this.shown) {
      if (entry.phase === "past" || active.has(key)) continue;
      if (this.lingers(entry.value)) this.shown.set(key, { value: entry.value, phase: "past", endedAt: at });
      else this.shown.delete(key);
      changed = true;
    }
    changed = this.expire(at) || changed;
    this.reschedule(at);
    if (changed) this.publish();
  }

  /** Drops everything shown, as when the presented scope changes. */
  clear(): void {
    if (this.disposed) return;
    this.clearTimer();
    if (this.shown.size === 0) return;
    this.shown = new Map();
    this.publish();
  }

  dispose(): void {
    this.disposed = true;
    this.clearTimer();
    this.shown = new Map();
  }

  private expire(at: number): boolean {
    let changed = false;
    for (const [key, entry] of this.shown) {
      if (entry.endedAt === null || at - entry.endedAt < this.lingerMs) continue;
      this.shown.delete(key);
      changed = true;
    }
    return changed;
  }

  private reschedule(at: number): void {
    this.clearTimer();
    let due = Number.POSITIVE_INFINITY;
    for (const entry of this.shown.values()) {
      if (entry.endedAt !== null) due = Math.min(due, entry.endedAt + this.lingerMs);
    }
    if (!Number.isFinite(due)) return;
    this.timer = this.schedule(() => {
      this.timer = null;
      if (this.disposed) return;
      const current = this.now();
      if (this.expire(current)) this.publish();
      this.reschedule(current);
    }, Math.max(0, due - at));
  }

  private clearTimer(): void {
    if (this.timer === null) return;
    this.clearSchedule(this.timer);
    this.timer = null;
  }

  private publish(): void {
    this.onChange(new Map(this.shown));
  }
}
