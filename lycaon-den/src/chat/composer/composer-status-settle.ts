import { createSignal, createEffect, on, onCleanup } from "solid-js";

/** Delay before removing a composer status surface so brief gaps don’t thrash layout. */
export const COMPOSER_STATUS_HIDE_SETTLE_MS = 480;

type ScheduleFn = typeof setTimeout;
type ClearScheduleFn = typeof clearTimeout;

/** Brief false intervals preserve the visible state. */
export class SettledVisibilityEngine {
  private shown = false;
  private timer: ReturnType<ScheduleFn> | undefined;

  constructor(
    private readonly settleMs: number,
    private readonly getDesired: () => boolean,
    private readonly onChange: () => void,
    private readonly schedule: ScheduleFn = setTimeout.bind(globalThis),
    private readonly clearSchedule: ClearScheduleFn = clearTimeout.bind(globalThis),
  ) {
    this.shown = this.getDesired();
  }

  read(): boolean {
    return this.shown;
  }

  handleDesired(desired: boolean): void {
    if (desired) {
      this.clearTimer();
      if (!this.shown) {
        this.shown = true;
        this.onChange();
      }
      return;
    }
    if (!this.shown || this.timer !== undefined) return;
    this.timer = this.schedule(() => {
      this.timer = undefined;
      if (this.getDesired()) return;
      this.shown = false;
      this.onChange();
    }, this.settleMs);
  }

  dispose(): void {
    this.clearTimer();
  }

  private clearTimer(): void {
    if (this.timer === undefined) return;
    this.clearSchedule(this.timer);
    this.timer = undefined;
  }
}

/** Visibility appears immediately and clears after a continuous false interval. */
export function createSettledVisibility(
  desired: () => boolean,
  settleMs = COMPOSER_STATUS_HIDE_SETTLE_MS,
): () => boolean {
  const [revision, setRevision] = createSignal(0);
  const bump = () => setRevision((n) => n + 1);
  const engine = new SettledVisibilityEngine(settleMs, desired, bump);

  createEffect(
    on(desired, (next) => {
      engine.handleDesired(next);
    }),
  );
  onCleanup(() => engine.dispose());

  return () => {
    revision();
    return engine.read();
  };
}
