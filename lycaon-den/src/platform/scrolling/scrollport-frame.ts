type Phase = "measure" | "observe" | "render";
type Request = { phase: Phase };

/** Geometry settles before scroll observers and the virtual window mutates its rows. */
class ScrollportFrame {
  private readonly pending = new Map<() => void, Request>();
  private frame: number | undefined;

  request(run: () => void, phase: Phase): void {
    if (this.pending.has(run)) return;
    this.pending.set(run, { phase });
    if (this.frame === undefined) this.frame = requestAnimationFrame(() => this.flush());
  }

  cancel(run: () => void): void {
    this.pending.delete(run);
    if (this.pending.size === 0) this.cancelFrame();
  }

  private cancelFrame(): void {
    if (this.frame !== undefined) cancelAnimationFrame(this.frame);
    this.frame = undefined;
  }

  flush(): void {
    this.cancelFrame();
    const batch = [...this.pending];
    try {
      for (const phase of ["measure", "observe", "render"] as const) {
        for (const [run, request] of batch) {
          if (request.phase !== phase || this.pending.get(run) !== request) continue;
          this.pending.delete(run);
          run();
        }
      }
    } finally {
      if (this.pending.size > 0 && this.frame === undefined) {
        this.frame = requestAnimationFrame(() => this.flush());
      }
    }
  }
}

const frames = new WeakMap<HTMLElement, ScrollportFrame>();

export function scheduleScrollportFrame(viewport: HTMLElement, phase: Phase, run: () => void): void {
  let frame = frames.get(viewport);
  if (!frame) {
    frame = new ScrollportFrame();
    frames.set(viewport, frame);
  }
  frame.request(run, phase);
}

export function cancelScrollportFrame(viewport: HTMLElement, run: () => void): void {
  frames.get(viewport)?.cancel(run);
}

export function flushScrollportFrameForTests(viewport: HTMLElement): void {
  frames.get(viewport)?.flush();
}
