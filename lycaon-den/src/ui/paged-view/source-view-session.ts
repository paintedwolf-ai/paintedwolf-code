import { traceSourceViewState } from "../../api/source-view-event-trace.ts";
import { LycaonApiError } from "../../api/http.ts";
import { withInterest, waitForRetry } from "./interest.ts";
import { ViewportInterest } from "./viewport-interest.ts";
import type { SourceViewsClient, SourceViewAnchor, SourceViewRowsQuery } from "../../api/source-views-client.ts";
import type { SourceView, SourceViewCreate, SourceViewEvent, SourcePresentation, SourceViewExtent, SourceViewFrame } from "../../api/types.ts";
import { PagedViewController, type FrameLimits } from "./controller.ts";

type Kind = SourceView["kind"];
export type ViewOf<K extends Kind> = Extract<SourceView, { kind: K }>;
export type FrameOf<K extends Kind> = Extract<SourceViewFrame, { kind: K }>;
const eventListeners = new Map<string, Set<() => void>>();
const projectListeners = new Map<string, Set<() => void>>();
const chatViews = new Map<string, Set<{ close(): Promise<void> }>>();
/** An attached view reads itself this often so the host keeps its lease. */
const LEASE_RENEWAL_MS = 60_000;
/** A preparing view is read until it settles: its readiness event can trail behind on a quiet stream. */
const PREPARING_REFRESH_MS = 1_000;

function sourceViewError(cause: unknown): Error {
  return cause instanceof Error ? cause : new Error("Could not prepare source.", { cause });
}

/** Work on a released view is superseded, never a failure of its own. */
function released(): DOMException {
  return new DOMException("This source view was released.", "AbortError");
}

function errorCode(error: unknown): string | undefined {
  return (error as { code?: string } | null)?.code;
}

/** The host no longer retains the handle; the same intent reopens it. */
function reopens(error: unknown): boolean {
  const code = errorCode(error);
  return code === "source_view_not_found" || code === "source_workspace_mismatch";
}

/** A deleted chat ends every view addressed by it. */
export function endSourceViewsForChat(sessionId: string): void {
  for (const session of [...chatViews.get(sessionId) ?? []]) void session.close().catch(() => undefined);
}

/** Notifications refresh requested state without changing displayed coordinates. */
export function receiveSourceViewEvent(event: SourceViewEvent): void {
  for (const listener of eventListeners.get(event.view_id) ?? []) listener();
}

/** Reconnects refresh every retained view for the project. */
export function resyncSourceViews(project: string): void {
  for (const listener of projectListeners.get(project) ?? []) listener();
}

/** Retains displayed coordinates while replacement intent is prepared. */
export class SourceViewSession<K extends Kind> {
  readonly frames: PagedViewController<FrameOf<K>>;
  private readonly viewportDemand: ViewportInterest;
  private value?: ViewOf<K>;
  private heldPresentation?: SourcePresentation;
  private paintedPresentation?: SourcePresentation;
  private presentationReferences = new Map<string, { users: number; retired?: SourcePresentation }>();
  private acquisitionIDs = new Map<string, string>();
  private pendingAcquisitions = 0;
  private acquisitions = new Map<string, Promise<SourcePresentation>>();
  private opening?: Promise<ViewOf<K>>;
  private refreshing?: Promise<ViewOf<K>>;
  private commands: Promise<unknown> = Promise.resolve();
  private listeners = new Set<() => void>();
  private generation = 0;
  private summaryVersion = 0;
  private mutating = false;
  private readonly lifetime = new AbortController();
  private queuedCommands = 0;
  private commandPhase: "idle" | "applying" | "initializing" | "persisting" = "idle";
  private pendingAcceptance?: ViewOf<K>;
  private active = 0;
  private closed = false;
  /**
   * True when the host may hold a newer state than `value`: the handle `open()` returned is
   * read once before it is trusted, a host event or stream resync said so, or a refresh ran
   * while another was in flight. Detaching alone never sets it; a lapsed lease is checked
   * when the view is next attached.
   */
  private dirty = true;
  private timer?: ReturnType<typeof setTimeout>;
  private refreshDue = 0;
  private subscribedId?: string;
  private errorValue?: unknown;
  /** Keeps painted rows available while replacement frames arrive. */
  private presentedFrames: readonly FrameOf<K>[] = [];
  private readonly invalidated = () => {
    if (this.errorValue) return;
    this.dirty = true;
    if (this.value) traceSourceViewState(this.value.id, this.value.state, "invalidated");
    this.scheduleRefresh();
  };

  constructor(readonly client: SourceViewsClient, readonly projectId: string,
    protected request: Extract<SourceViewCreate, { kind: K }>,
    cost: (frame: FrameOf<K>) => { rows: number; bytes: number }, limits: Partial<FrameLimits> = {}) {
    this.viewportDemand = new ViewportInterest(client, projectId);
    this.frames = new PagedViewController(cost, limits);
    this.frames.detach();
    this.frames.subscribe(() => {
      const frames = this.frames.frames();
      if (frames.length) this.presentedFrames = frames;
      this.notify();
    });
    const chat = request.session_id?.trim();
    if (chat) {
      let views = chatViews.get(chat);
      if (!views) { views = new Set(); chatViews.set(chat, views); }
      views.add(this);
    }
  }

  protected get attached(): boolean { return this.active > 0; }

  isReleased(): boolean { return this.closed; }

  /** The chat that addressed this view was deleted: the view ends with it. */
  private endedByChat(error: unknown): DOMException | undefined {
    if (errorCode(error) !== "session_not_found") return undefined;
    void this.close().catch(() => undefined);
    return released();
  }

  state(): ViewOf<K> | undefined { return this.value; }

  presentation(): { id?: string; state?: ViewOf<K>; frames: readonly FrameOf<K>[] } {
    const held = this.paintedPresentation?.view;
    if (!held) return { state: this.value, frames: [] };
    return { id: this.paintedPresentation?.id, state: held as ViewOf<K>, frames: this.presentedFrames.filter(frame => frame.view_id === held.id) };
  }

  /** The host resolves a presentation only inside its own view. */
  protected presentationOf(id: string | null | undefined, viewId: string): boolean {
    return !!id && [this.paintedPresentation, this.heldPresentation].some(held => held?.id === id && held.view.id === viewId);
  }

  /** Displayed rows and queued commands retain their coordinate resource. */
  retainPresentation(id: string): () => void {
    const entry = this.presentationReferences.get(id) ?? { users: 0 };
    this.presentationReferences.set(id, entry);
    entry.users++;
    let retained = true;
    return () => {
      if (!retained) return;
      retained = false;
      if (--entry.users > 0) return;
      this.presentationReferences.delete(id);
      if (entry.retired) this.retirePresentation(entry.retired);
    };
  }

  private retirePresentation(presentation: SourcePresentation): void {
    const entry = this.presentationReferences.get(presentation.id);
    if (entry?.users) { entry.retired = presentation; return; }
    queueMicrotask(() => {
      if (this.closed) return;
      const current = this.presentationReferences.get(presentation.id);
      if (current?.users) return;
      void this.client.releaseSourcePresentation(this.projectId, presentation.view.id, presentation.id).catch(() => undefined);
    });
  }

  /** Cache reads use the displayed coordinates. */
  protected basisFor(state: ViewOf<K>): { revision: string; extent: SourceViewExtent } {
    const live = this.frames.presented();
    const held = this.frames.frames()[0];
    if (live && held && live.viewId === state.id) {
      return { revision: live.projectionRevision, extent: held.extent };
    }
    return { revision: state.projection_revision, extent: state.extent };
  }

  updateViewport(start: number, end: number, direction = 0): void {
    const presentation = this.paintedPresentation;
    if (presentation) this.viewportDemand.update(presentation.view.id, presentation.id, start, end, direction);
  }

  clearViewport(): void { this.viewportDemand.clear(); }

  error(): unknown { return this.errorValue; }
  activity() { return { queuedCommands: this.queuedCommands, commandPhase: this.commandPhase,
    pendingAcceptance: this.pendingAcceptance?.intent_revision, acquiringPresentations: this.pendingAcquisitions }; }

  protected interested<T>(work: Promise<T>, signal?: AbortSignal): Promise<T> {
    return withInterest(work, signal, this.lifetime.signal);
  }
  subscribe(listener: () => void): () => void { this.listeners.add(listener); return () => this.listeners.delete(listener); }

  attach(): () => void {
    if (this.closed) throw released();
    const first = this.active++ === 0;
    if (!this.mutating) this.frames.attach();
    if (first && this.leaseLapsed()) this.dirty = true;
    void (first && this.dirty ? this.refresh() : this.open()).catch(error => this.report(error));
    let attached = true;
    return () => {
      if (!attached) return;
      attached = false;
      if (--this.active === 0) {
        this.clearViewport();
        this.frames.detach();
        clearTimeout(this.timer); this.timer = undefined;
      }
    };
  }

  /** The host retires an idle view at `expires_at`; nothing renewed it while detached. */
  private leaseLapsed(): boolean {
    if (!this.value) return false;
    const expiresAt = Date.parse(this.value.expires_at);
    return Number.isFinite(expiresAt) && expiresAt - Date.now() <= LEASE_RENEWAL_MS;
  }

  async open(): Promise<ViewOf<K>> {
    if (this.closed) throw released();
    if (this.value) return this.value;
    if (this.opening) return this.opening;
    this.opening = this.client.createSourceView(this.projectId, this.request).then(async state => {
      if (this.closed) {
        await this.client.releaseSourceView(this.projectId, state.id);
        throw released();
      }
      return this.install(state);
    }, error => { throw this.endedByChat(error) ?? error; }).finally(() => { this.opening = undefined; });
    return this.opening;
  }

  /** Preparation can outlive its presenter; each waiter has an independent interest. */
  async ready(signal?: AbortSignal): Promise<ViewOf<K>> {
    return this.waitUntilReady(signal);
  }

  private async waitUntilReady(signal?: AbortSignal): Promise<ViewOf<K>> {
    signal?.throwIfAborted();
    const detach = this.attach();
    try {
      await this.interested(this.open(), signal);
      signal?.throwIfAborted();
      return await new Promise<ViewOf<K>>((resolve, reject) => {
        let unsubscribe = () => {};
        const finish = () => { unsubscribe(); signal?.removeEventListener("abort", abort); };
        const abort = () => { finish(); reject(new DOMException("Canceled", "AbortError")); };
        const check = () => {
          const current = this.value;
          if (current?.state === "ready" && !this.mutating && !this.pendingAcceptance && !this.errorValue) { finish(); resolve(current); }
          else if (current?.state === "failed" || this.errorValue) {
            finish(); reject(this.errorValue ? sourceViewError(this.errorValue) : new Error(current?.failure?.message ?? "Could not prepare source."));
          }
        };
        unsubscribe = this.subscribe(check);
        signal?.addEventListener("abort", abort, { once: true });
        if (signal?.aborted) abort(); else check();
      });
    } finally { detach(); }
  }

  refresh(): Promise<ViewOf<K>> {
    if (this.refreshing) { this.dirty = true; return this.refreshing; }
    this.dirty = false;
    const generation = this.generation;
    this.refreshing = this.open().then(async state => {
      const latest = await this.client.getSourceView(this.projectId, state.id);
      if (generation !== this.generation || this.closed || this.mutating || this.pendingAcceptance) return this.value ?? state;
      return this.install(latest);
    }).catch(async error => {
      if (generation !== this.generation || this.closed || this.mutating || this.pendingAcceptance) return this.value ?? await this.open();
      const ended = this.endedByChat(error);
      if (ended) throw ended;
      if (!reopens(error)) throw error;
      return this.replaceExpired(this.value);
    }).finally(() => {
      this.refreshing = undefined;
      this.scheduleRefresh();
    });
    return this.refreshing;
  }

  private async replaceExpired(previous?: ViewOf<K>): Promise<ViewOf<K>> {
    const generation = ++this.generation;
    const request = await this.replacementRequest(previous);
    if (generation !== this.generation || this.closed) return this.open();
    this.request = request;
    this.releasePresentation();
    this.value = undefined;
    return this.open();
  }

  /** Admission is synchronous; accepted mutations survive cancellation of their caller. */
  mutate(apply: (state: ViewOf<K>) => Promise<SourceView>, options: { signal?: AbortSignal; restartFailed?: boolean; release?: () => void } = {}): Promise<ViewOf<K>> {
    this.queuedCommands++;
    const result = this.commands.then(async () => {
      options.signal?.throwIfAborted();
      this.lifetime.signal.throwIfAborted();
      this.generation++;
      this.mutating = true;
      try {
        if (this.pendingAcceptance) await this.publishAccepted(this.pendingAcceptance);
        const state = await this.interested(this.open(), options.signal);
        const accepted = await this.applyCommand(apply, state, options.signal, options.restartFailed);
        return await this.publishAccepted(accepted);
      } catch (error) {
        if ((error as { name?: string }).name !== "AbortError") this.report(error);
        throw error;
      } finally {
        this.commandPhase = "idle";
        this.mutating = false;
        if (this.active) this.frames.attach();
        this.notify(); this.scheduleRefresh();
      }
    }).finally(() => { this.queuedCommands--; options.release?.(); this.notify(); });
    this.commands = result.catch(() => undefined);
    return this.interested(result, options.signal);
  }

  private async applyCommand(apply: (state: ViewOf<K>) => Promise<SourceView>, initial: ViewOf<K>, signal?: AbortSignal, restartFailed = false): Promise<ViewOf<K>> {
    let state = initial;
    let recoveries = 0;
    for (;;) {
      signal?.throwIfAborted();
      this.lifetime.signal.throwIfAborted();
      if (restartFailed && state.state === "failed") {
        if (++recoveries > 2) throw new Error(state.failure?.message ?? "Could not prepare source.");
        state = await this.replaceView(state);
        signal?.throwIfAborted();
      }
      this.commandPhase = "applying";
      try { return this.checkedState(await apply(state)); }
      catch (error) {
        signal?.throwIfAborted();
        const code = errorCode(error);
        const ended = this.endedByChat(error);
        if (ended) throw ended;
        if (code === "source_view_preparing") {
          if (state.state === "failed") throw new Error(state.failure?.message ?? "Could not prepare source.");
          this.commandPhase = "initializing";
          await this.interested(waitForRetry(this.lifetime.signal), signal);
          state = this.checkedState(await this.interested(this.client.getSourceView(this.projectId, state.id), signal));
        } else if (++recoveries <= 2 && code === "source_view_revision_changed") {
          state = this.checkedState(await this.interested(this.client.getSourceView(this.projectId, state.id), signal));
        } else if (recoveries <= 2 && reopens(error)) {
          state = await this.replaceExpired(state);
        } else throw error;
      }
    }
  }

  /** Retries the durable acceptance before asking the host to do more work. */
  retry(): Promise<ViewOf<K>> {
    return this.mutate(async state => {
      const current = this.checkedState(await this.client.getSourceView(this.projectId, state.id));
      return current.state === "failed" ? this.replaceView(current) : current;
    });
  }

  private async publishAccepted(state: ViewOf<K>): Promise<ViewOf<K>> {
    this.pendingAcceptance = state;
    this.commandPhase = "persisting";
    await this.saveAcceptedIntent(state);
    this.pendingAcceptance = undefined;
    this.generation++;
    return this.install(state);
  }

  /** A strict read in the presented coordinates; a resident frame answers without the host. */
  async frame(offset: number, limit = 200, signal?: AbortSignal): Promise<FrameOf<K>> {
    this.updateViewport(offset, offset + limit);
    signal?.throwIfAborted();
    const current = this.value;
    const basis = current ? this.basisFor(current) : undefined;
    const resident = current && basis && this.frames.frames().find(frame => frame.view_id === current.id
      && frame.projection_revision === basis.revision
      && frame.rows.length <= limit && frame.span.start === offset
      && frame.span.end >= Math.min(offset + limit, frame.extent.rows));
    if (resident) return resident;
    return this.loadFrame({ offset, limit }, signal);
  }

  async frameAt(anchor: SourceViewAnchor, signal?: AbortSignal, before = 0, offset = 0,
    coordinates: "latest" | "presented" = "latest"): Promise<FrameOf<K>> {
    return this.loadFrame({ anchor, ...(offset ? { offset } : {}), ...(before ? { before } : {}), limit: 200,
      ...(coordinates === "latest" ? { follow: "latest" as const } : {}) }, signal);
  }

  protected async acquirePresentation(state: ViewOf<K>, latest = false, signal?: AbortSignal): Promise<SourcePresentation> {
    if (this.heldPresentation?.view.id === state.id && (!latest ||
      this.heldPresentation.view.intent_revision === state.intent_revision && this.heldPresentation.view.projection_revision === state.projection_revision)) return this.heldPresentation;
    const key = `${state.id}:${state.intent_revision}:${state.projection_revision}`;
    let acquiring = this.acquisitions.get(key);
    if (!acquiring) {
      const requestID = this.acquisitionIDs.get(key) ?? crypto.randomUUID();
      this.acquisitionIDs.set(key, requestID);
      this.pendingAcquisitions++;
      acquiring = this.client.createSourcePresentation(this.projectId, state.id,
        { operation_id: requestID, intent_revision: state.intent_revision }).then(async presentation => {
        if (this.closed || this.acquisitions.get(key) !== acquiring || this.value?.id !== state.id || this.value.intent_revision !== state.intent_revision) {
          await this.client.releaseSourcePresentation(this.projectId, state.id, presentation.id);
          throw new DOMException("The presentation was superseded.", "AbortError");
        }
        return presentation;
      }).catch(error => { this.acquisitions.delete(key); throw error; })
        .finally(() => { this.pendingAcquisitions--; this.notify(); });
      this.acquisitions.set(key, acquiring);
    }
    return this.interested(acquiring, signal);
  }

  protected async loadFrame(query: { offset?: number; anchor?: SourceViewAnchor; before?: number; limit: number; follow?: "latest" }, signal?: AbortSignal,
    options: { foreground?: boolean } = {}): Promise<FrameOf<K>> {
    return this.readWithRecovery(async state => {
      const presentation = await this.acquirePresentation(query.follow ? await this.ready(signal) : state, !!query.follow, signal);
      signal?.throwIfAborted();
      if (!query.follow && this.heldPresentation && this.heldPresentation.id !== presentation.id) throw new DOMException("The presentation was superseded.", "AbortError");
      const retained = presentation.view;
      const basis = retained.projection_revision;
      const retention = query.follow ? this.frameRetention(state) : {};
      const key = `${presentation.id}:${JSON.stringify(query)}`;
      const result = await this.frames.load(key, async requestSignal => {
        const frame = await this.client.getSourceViewRows(this.projectId, state.id,
          { presentation_id: presentation.id, offset: query.offset, anchor: query.anchor, context_before: query.before, limit: query.limit, ...retention.query }, requestSignal);
        if (frame.kind !== this.request.kind || frame.view_id !== retained.id || frame.intent_revision !== retained.intent_revision || frame.projection_revision !== basis) {
          throw new Error("The source frame does not match its presentation.");
        }
        this.validateFrame(frame as FrameOf<K>);
        if (query.follow && this.value?.intent_revision !== retained.intent_revision) throw new DOMException("The presentation was superseded.", "AbortError");
        return frame as FrameOf<K>;
      }, signal, { identity: frame => ({ viewId: frame.view_id, intentRevision: frame.intent_revision, projectionRevision: frame.projection_revision }),
        follow: true, retain: retention.frames, accept: () => this.acceptPresentation(presentation) });
      return result;
    }, signal, options.foreground ?? query.follow === "latest");
  }

  /** Expired leases are reacquired without changing the requested intent. */
  protected async readWithRecovery<T>(read: (state: ViewOf<K>) => Promise<T>, signal?: AbortSignal, foreground = true): Promise<T> {
    let recoveries = 0;
    for (;;) {
      signal?.throwIfAborted();
      try { return await read(await this.interested(foreground && !this.heldPresentation ? this.ready(signal) : this.open(), signal)); }
      catch (error) {
        signal?.throwIfAborted();
        const ended = this.endedByChat(error);
        if (ended) throw ended;
        if (this.closed || this.active === 0) throw error;
        const code = errorCode(error);
        if (code === "source_view_not_found") {
          if (++recoveries > 2) throw error;
          this.releasePresentation(); await this.refresh(); continue;
        }
        if (!foreground || (code !== "source_view_preparing" && code !== "source_view_revision_changed" &&
          !(error instanceof DOMException && error.name === "AbortError"))) throw error;
        await this.revalidate(signal);
      }
    }
  }

  /** The host stopped answering in the presented coordinates; reads continue in the head. */
  protected releasePresentation(): void {
    for (const pending of this.acquisitions.values()) void pending.then(value =>
      this.retirePresentation(value)).catch(() => undefined);
    this.acquisitions.clear(); this.acquisitionIDs.clear();
    this.heldPresentation = undefined;
    this.frames.invalidate();
  }

  private acceptPresentation(presentation: SourcePresentation): void {
    if (this.heldPresentation?.id === presentation.id) return;
    this.heldPresentation = presentation;
    this.paintedPresentation = presentation;
    const key = `${presentation.view.id}:${presentation.view.intent_revision}:${presentation.view.projection_revision}`;
    for (const [other, pending] of this.acquisitions) {
      if (other === key) continue;
      this.acquisitions.delete(other); this.acquisitionIDs.delete(other);
      void pending.then(value => {
        if (value.id !== presentation.id) this.retirePresentation(value);
      }).catch(() => undefined);
    }
    if (!this.acquisitions.has(key)) this.acquisitions.set(key, Promise.resolve(presentation));
  }

  protected async revalidate(signal?: AbortSignal): Promise<void> {
    signal?.throwIfAborted();
    const version = this.summaryVersion;
    const detach = this.attach();
    try {
      this.invalidated();
      await new Promise<void>((resolve, reject) => {
        let unsubscribe = () => {};
        const finish = () => { unsubscribe(); signal?.removeEventListener("abort", abort); };
        const abort = () => { finish(); reject(new DOMException("Canceled", "AbortError")); };
        const check = () => {
          if (this.closed || this.errorValue) { finish(); reject(sourceViewError(this.errorValue)); }
          else if (this.summaryVersion !== version) { finish(); resolve(); }
        };
        unsubscribe = this.subscribe(check);
        signal?.addEventListener("abort", abort, { once: true });
        if (signal?.aborted) abort(); else check();
      });
    } finally { detach(); }
  }

  async changed(revision: string, signal?: AbortSignal): Promise<void> {
    if (this.closed || signal?.aborted) throw new DOMException("Canceled", "AbortError");
    if (this.value?.projection_revision !== revision) return;
    return new Promise((resolve, reject) => {
      let unsubscribe = () => {};
      const finish = () => { unsubscribe(); signal?.removeEventListener("abort", abort); };
      const abort = () => { finish(); reject(new DOMException("Canceled", "AbortError")); };
      const check = () => {
        if (this.closed || this.errorValue) { finish(); reject(sourceViewError(this.errorValue)); }
        else if (this.value?.projection_revision !== revision) { finish(); resolve(); }
      };
      unsubscribe = this.subscribe(check);
      signal?.addEventListener("abort", abort, { once: true });
      check();
    });
  }

  invalidate(): void { this.invalidated(); }

  /** Replacement stays inside the command queue and does not await recursive coverage. */
  private async replaceView(previous: ViewOf<K>): Promise<ViewOf<K>> {
    this.generation++;
    clearTimeout(this.timer); this.timer = undefined;
    this.unsubscribeEvents();
    this.request = await this.replacementRequest(previous);
    this.value = undefined; this.errorValue = undefined; this.dirty = true;
    this.releasePresentation();
    const next = await this.open();
    void this.client.releaseSourceView(this.projectId, previous.id).catch(() => undefined);
    return next;
  }

  async close(): Promise<void> {
    if (this.closed) return;
    const id = this.value?.id;
    this.forget();
    if (!id) return;
    try {
      await this.client.releaseSourceView(this.projectId, id);
    } catch (error) {
      // A view the host no longer retains is already released.
      if (errorCode(error) !== "source_view_not_found") throw error;
    }
  }

  /** Disposes the local presenter while transferring its host handle. */
  forget(): void {
    if (this.closed) return;
    this.closed = true;
    const chat = this.request.session_id?.trim();
    const views = chat ? chatViews.get(chat) : undefined;
    if (chat && views?.delete(this) && !views.size) chatViews.delete(chat);
    this.lifetime.abort();
    this.clearViewport();
    this.generation++;
    clearTimeout(this.timer);
    this.unsubscribeEvents();
    this.errorValue = released();
    for (const pending of this.acquisitions.values()) void pending.then(value =>
      this.retirePresentation(value)).catch(() => undefined);
    this.acquisitions.clear(); this.acquisitionIDs.clear(); this.heldPresentation = undefined; this.paintedPresentation = undefined;
    this.presentedFrames = [];
    this.frames.close();
    this.notify();
    this.listeners.clear();
  }

  protected replacementRequest(previous?: ViewOf<K>): Extract<SourceViewCreate, { kind: K }> | Promise<Extract<SourceViewCreate, { kind: K }>> {
    return { ...this.request, ...(previous ? { intent: previous.intent } : {}), operation_id: crypto.randomUUID() } as typeof this.request;
  }

  protected frameRetention(_state: ViewOf<K>): { query?: Partial<SourceViewRowsQuery>; frames?: (frame: FrameOf<K>) => readonly FrameOf<K>[] } { return {}; }

  protected validateFrame(_frame: FrameOf<K>): void {}

  protected validateState(_state: ViewOf<K>): void {}

  protected async saveAcceptedIntent(_state: ViewOf<K>): Promise<void> {}

  private checkedState(state: SourceView): ViewOf<K> {
    if (state.kind !== this.request.kind) throw new Error("The source view has the wrong kind.");
    if (this.closed) throw released();
    this.validateState(state as ViewOf<K>);
    return state as ViewOf<K>;
  }

  protected install(state: SourceView): ViewOf<K> {
    const checked = this.checkedState(state);
    this.summaryVersion++;
    this.value = checked;
    traceSourceViewState(checked.id, checked.state, "installed");
    this.errorValue = undefined;
    if (this.subscribedId !== state.id) {
      this.unsubscribeEvents();
      let listeners = eventListeners.get(state.id);
      if (!listeners) { listeners = new Set(); eventListeners.set(state.id, listeners); }
      listeners.add(this.invalidated); this.subscribedId = state.id;
      let project = projectListeners.get(this.projectId);
      if (!project) { project = new Set(); projectListeners.set(this.projectId, project); }
      project.add(this.invalidated);
    }
    if (!this.frames.presented()) this.frames.bind({ viewId: state.id, intentRevision: state.intent_revision, projectionRevision: state.projection_revision });
    this.notify(); this.scheduleRefresh();
    return this.value;
  }
  private unsubscribeEvents(): void {
    if (!this.subscribedId) return;
    const listeners = eventListeners.get(this.subscribedId);
    listeners?.delete(this.invalidated);
    if (!listeners?.size) eventListeners.delete(this.subscribedId);
    this.subscribedId = undefined;
    const project = projectListeners.get(this.projectId);
    project?.delete(this.invalidated);
    if (!project?.size) projectListeners.delete(this.projectId);
  }
  /** Host events carry every change; the idle read renews the lease. */
  private scheduleRefresh(): void {
    if (!this.active || this.closed || this.errorValue) return;
    const delay = this.dirty ? 250 : this.value?.state === "preparing" ? PREPARING_REFRESH_MS : LEASE_RENEWAL_MS;
    const due = Date.now() + delay;
    if (this.timer && this.refreshDue <= due) return;
    clearTimeout(this.timer);
    this.refreshDue = due;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.refresh().catch(error => this.report(error));
    }, delay);
  }
  private report(error: unknown): void {
    // Admission refusal is not a view failure; the next refresh asks again.
    if (error instanceof LycaonApiError && error.code === "rate_limited") { this.dirty = true; this.scheduleRefresh(); return; }
    this.dirty = false;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.errorValue = error;
    this.notify();
  }
  private notify(): void { for (const listener of this.listeners) listener(); }
}
