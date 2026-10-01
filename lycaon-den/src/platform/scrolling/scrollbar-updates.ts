type PendingUpdate = object;
type UpdateFrame = { id: number };

/** Coalesces ready hosts into one frame; blocked hosts stay pending. */
export class ScrollbarUpdates {
  private readonly pending = new Map<HTMLElement, PendingUpdate>();
  private frame: UpdateFrame | undefined;

  constructor(
    private readonly canUpdate: (host: HTMLElement) => boolean,
    private readonly update: (host: HTMLElement) => void,
  ) {}

  request(host: HTMLElement): void {
    this.pending.set(host, {});
    this.resume(host);
  }

  resume(host: HTMLElement): void {
    if (this.pending.has(host) && this.canUpdate(host)) this.schedule();
  }

  resumeConnected(): void {
    if (this.frame) return;
    for (const host of this.pending.keys()) {
      if (!this.canUpdate(host)) continue;
      this.schedule();
      return;
    }
  }

  forget(host: HTMLElement): void {
    this.pending.delete(host);
    if (this.pending.size === 0) this.cancelFrame();
  }

  clear(): void {
    this.pending.clear();
    this.cancelFrame();
  }

  private cancelFrame(): void {
    if (this.frame) cancelAnimationFrame(this.frame.id);
    this.frame = undefined;
  }

  private schedule(): void {
    if (this.frame) return;
    const frame: UpdateFrame = { id: 0 };
    this.frame = frame;
    frame.id = requestAnimationFrame(() => {
      if (this.frame !== frame) return;
      this.frame = undefined;
      // Requests added by callbacks wait for the next frame.
      for (const [host, request] of [...this.pending]) {
        if (this.pending.get(host) !== request || !this.canUpdate(host)) continue;
        this.pending.delete(host);
        this.update(host);
      }
    });
  }
}
