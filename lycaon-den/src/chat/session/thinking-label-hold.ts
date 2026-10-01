import { createSignal, on, createEffect, onCleanup } from "solid-js";

export const THINKING_LABEL_HOLD_MS = 5000;

export type ThinkingLabelHoldOptions = {
  visible: () => boolean;
  resolveLabel: () => string;
  /** Host-measured progress is already throttled at its source. */
  immediate?: () => boolean;
  holdMs?: number;
};

type ScheduleFn = typeof setInterval;
type ClearScheduleFn = typeof clearInterval;

/** Imperative label hold engine. */
export class ThinkingLabelHoldEngine {
  private displayed: string | undefined;
  private timer: ReturnType<ScheduleFn> | undefined;
  private cycleActive = false;

  constructor(
    private readonly holdMs: number,
    private readonly getVisible: () => boolean,
    private readonly resolveLabel: () => string,
    private readonly onChange: () => void,
    // Bind timer globals because calling them through the engine changes their receiver.
    private readonly schedule: ScheduleFn = setInterval.bind(globalThis),
    private readonly clearSchedule: ClearScheduleFn = clearInterval.bind(globalThis),
  ) {}

  /** Label inputs do not reset the hold timer while visible. */
  handleVisibilityChange(visible: boolean): void {
    if (!visible) {
      this.stopCycle();
      this.displayed = undefined;
      this.onChange();
      return;
    }

    if (this.cycleActive) return;
    this.startCycle();
  }

  read(): string | undefined {
    if (!this.getVisible()) return undefined;
    return this.displayed;
  }

  dispose(): void {
    this.stopCycle();
    this.displayed = undefined;
  }

  private startCycle(): void {
    this.cycleActive = true;
    this.publishCurrentLabel();
    this.timer = this.schedule(() => {
      if (!this.getVisible()) return;
      this.publishCurrentLabel();
    }, this.holdMs);
  }

  private stopCycle(): void {
    this.cycleActive = false;
    this.clearTimer();
  }

  private publishCurrentLabel(): void {
    this.displayed = this.resolveLabel();
    this.onChange();
  }

  private clearTimer(): void {
    if (this.timer !== undefined) {
      this.clearSchedule(this.timer);
      this.timer = undefined;
    }
  }
}

/** Holds a label for one interval and clears it when hidden. */
export function createThinkingLabelHold(
  options: ThinkingLabelHoldOptions,
): () => string | undefined {
  const holdMs = options.holdMs ?? THINKING_LABEL_HOLD_MS;
  const [revision, setRevision] = createSignal(0);
  const bump = () => setRevision((n) => n + 1);

  const engine = new ThinkingLabelHoldEngine(
    holdMs,
    options.visible,
    options.resolveLabel,
    bump,
  );

  createEffect(
    on(options.visible, (visible, prevVisible) => {
      if (visible === prevVisible) return;
      engine.handleVisibilityChange(visible);
    }),
  );
  onCleanup(() => engine.dispose());

  return () => {
    // Reading the revision refreshes the label after each hold interval.
    revision();
    if (!options.visible()) return undefined;
    if (options.immediate?.()) return options.resolveLabel();
    return engine.read();
  };
}
