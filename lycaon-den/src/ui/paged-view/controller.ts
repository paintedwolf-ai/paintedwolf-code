export type FrameCost = { rows: number; bytes: number };
export type FrameIdentity = { viewId: string; intentRevision: string; projectionRevision: string };
export type FrameLimits = { rows: number; bytes: number; pages: number; concurrent: number };
export const DEFAULT_FRAME_LIMITS: FrameLimits = { rows: 2_000, bytes: 8 * 1024 * 1024, pages: 32, concurrent: 2 };

type FrameReadOptions<F> = {
  identity?: (frame: F) => FrameIdentity;
  follow?: boolean;
  retain?: (frame: F) => readonly F[];
  accept?: (frame: F) => void;
};

type Cached<F> = { frame: F; cost: FrameCost };
type Waiter<F> = { resolve(frame: F): void; reject(error: unknown): void; removeAbort(): void };
type Pending<F> = {
  key: string;
  generation: number;
  basis?: FrameIdentity;
  controller: AbortController;
  load(signal: AbortSignal): Promise<F>;
  options: FrameReadOptions<F>;
  waiters: Set<Waiter<F>>;
  running: boolean;
};

function aborted(): DOMException { return new DOMException("The presentation request was canceled.", "AbortError"); }

function sameIdentity(a: FrameIdentity | undefined, b: FrameIdentity): boolean {
  return a?.viewId === b.viewId && a.intentRevision === b.intentRevision && a.projectionRevision === b.projectionRevision;
}

/** Retains bounded frames in one immutable presentation. */
export class PagedViewController<F> {
  private readonly cache = new Map<string, Cached<F>>();
  private readonly pending = new Map<string, Pending<F>>();
  private readonly listeners = new Set<() => void>();
  private readonly limits: FrameLimits;
  private retained: FrameCost = { rows: 0, bytes: 0 };
  private generation = 0;
  private running = 0;
  private active = true;
  private closed = false;
  private identity?: FrameIdentity;

  constructor(private readonly describe: (frame: F) => FrameCost, limits: Partial<FrameLimits> = {}) {
    this.limits = { ...DEFAULT_FRAME_LIMITS, ...limits };
    for (const value of Object.values(this.limits)) {
      if (!Number.isSafeInteger(value) || value <= 0) throw new Error("Invalid presentation cache limit.");
    }
  }

  get usage(): Readonly<FrameCost & { pages: number; pending: number }> {
    return { ...this.retained, pages: this.cache.size, pending: this.pending.size };
  }

  /** The coordinates the retained frames are in, once any frame has been adopted. */
  presented(): FrameIdentity | undefined { return this.cache.size ? this.identity : undefined; }

  /** Seeds empty coordinates without replacing painted frames. */
  bind(identity: FrameIdentity): void {
    if (this.closed) throw aborted();
    const sameIntent = this.identity?.viewId === identity.viewId && this.identity.intentRevision === identity.intentRevision;
    if (sameIntent) {
      if (!this.cache.size) this.identity = { ...identity };
      return;
    }
    this.cancelRequests();
    this.cache.clear(); this.retained = { rows: 0, bytes: 0 };
    this.identity = { ...identity };
    this.notify();
  }

  subscribe(listener: () => void): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  frames(): readonly F[] { return [...this.cache.values()].map(entry => entry.frame); }

  peek(key: string): F | undefined {
    const held = this.cache.get(key);
    if (!held) return undefined;
    this.cache.delete(key); this.cache.set(key, held);
    return held.frame;
  }

  load(key: string, load: (signal: AbortSignal) => Promise<F>, signal?: AbortSignal, options: FrameReadOptions<F> = {}): Promise<F> {
    if (this.closed || !this.active || signal?.aborted) return Promise.reject(aborted());
    const cached = this.peek(key);
    if (cached !== undefined) return Promise.resolve(cached);
    let request = this.pending.get(key);
    if (!request) {
      request = { key, generation: this.generation, basis: this.identity, controller: new AbortController(), load, options, waiters: new Set(), running: false };
      this.pending.set(key, request);
    }
    const target = request;
    const promise = new Promise<F>((resolve, reject) => {
      const cancel = () => {
        target.waiters.delete(waiter); waiter.removeAbort(); reject(aborted());
        if (!target.waiters.size) {
          target.controller.abort();
          if (this.pending.get(key) === target) this.pending.delete(key);
        }
      };
      const waiter: Waiter<F> = { resolve, reject, removeAbort: () => signal?.removeEventListener("abort", cancel) };
      target.waiters.add(waiter);
      signal?.addEventListener("abort", cancel, { once: true });
    });
    this.schedule();
    return promise;
  }

  /** Detaching cancels viewport work; it does not release or cancel host operations. */
  detach(): void { this.active = false; this.cancelRequests(); }
  attach(): void { if (!this.closed) { this.active = true; this.schedule(); } }

  invalidate(): void {
    this.cancelRequests();
    this.cache.clear(); this.retained = { rows: 0, bytes: 0 };
    this.notify();
  }

  close(): void { this.closed = true; this.invalidate(); this.listeners.clear(); }

  private cancelRequests(): void {
    this.generation++;
    for (const request of this.pending.values()) {
      request.controller.abort(); this.settle(request, undefined, aborted());
      this.pending.delete(request.key);
    }
  }

  private schedule(): void {
    if (!this.active || this.closed) return;
    for (const request of this.pending.values()) {
      if (this.running >= this.limits.concurrent) break;
      if (request.running || !request.waiters.size) continue;
      request.running = true; this.running++;
      void this.run(request);
    }
  }

  /** Adoption cancels reads in the outgoing coordinates. */
  private async run(request: Pending<F>): Promise<void> {
    try {
      const frame = await request.load(request.controller.signal);
      if (request.generation !== this.generation || request.controller.signal.aborted) throw aborted();
      const cost = this.frameCost(frame);
      const retained = (request.options.retain?.(frame) ?? []).map(frame => ({ frame, cost: this.frameCost(frame) }));
      const identity = request.options.identity?.(frame);
      if (identity && !sameIdentity(this.identity, identity)) {
        this.generation++;
        for (const pending of this.pending.values()) {
          if (pending === request) continue;
          if (sameIdentity(pending.basis, identity)) { pending.generation = this.generation; continue; }
          pending.controller.abort(); this.settle(pending, undefined, aborted());
          this.pending.delete(pending.key);
        }
        this.cache.clear(); this.retained = { rows: 0, bytes: 0 };
        this.identity = identity;
      }
      if (!this.cache.size) {
        for (const [index, entry] of retained.entries()) {
          this.cacheFrame(`retained:${index}`, entry.frame, entry.cost);
        }
      }
      request.options.accept?.(frame);
      this.cacheFrame(request.key, frame, cost);
      this.settle(request, frame); this.notify();
    } catch (error) { this.settle(request, undefined, error); }
    finally {
      if (this.pending.get(request.key) === request) this.pending.delete(request.key);
      this.running--; this.schedule();
    }
  }

  private frameCost(frame: F): FrameCost {
    const cost = this.describe(frame);
    if (!Number.isSafeInteger(cost.rows) || !Number.isSafeInteger(cost.bytes) || cost.rows < 0 || cost.bytes < 0
      || cost.rows > this.limits.rows || cost.bytes > this.limits.bytes) throw new Error("The presentation frame exceeds its cache limit.");
    return cost;
  }

  private cacheFrame(key: string, frame: F, cost: FrameCost): void {
    const previous = this.cache.get(key);
    if (previous) { this.retained.rows -= previous.cost.rows; this.retained.bytes -= previous.cost.bytes; this.cache.delete(key); }
    this.cache.set(key, { frame, cost });
    this.retained.rows += cost.rows; this.retained.bytes += cost.bytes;
    while (this.cache.size > this.limits.pages || this.retained.rows > this.limits.rows || this.retained.bytes > this.limits.bytes) {
      const oldest = this.cache.entries().next().value!;
      this.cache.delete(oldest[0]);
      this.retained.rows -= oldest[1].cost.rows; this.retained.bytes -= oldest[1].cost.bytes;
    }
  }

  private settle(request: Pending<F>, frame?: F, error?: unknown): void {
    for (const waiter of request.waiters) {
      waiter.removeAbort();
      if (error !== undefined) waiter.reject(error); else waiter.resolve(frame!);
    }
    request.waiters.clear();
  }

  private notify(): void { for (const listener of this.listeners) listener(); }
}
