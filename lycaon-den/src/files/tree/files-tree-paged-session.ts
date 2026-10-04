import type { SourceViewsClient } from "../../api/source-views-client.ts";
import type { SourceTreeAddress, SourceTreeCommand, SourceTreeFrame, SourceTreeIntent, SourceTreeView } from "../../api/types.ts";
import { treeViewportLeadingRows } from "./files-tree-viewport.ts";
import { UNKNOWN_SUBTREE_END } from "./files-tree-paged-model.ts";
import { treeLayoutRange } from "./files-tree-sticky.ts";
import { saveFilesTreeIntent } from "./files-tree-intent-state.ts";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { SourceViewSession } from "../../ui/paged-view/source-view-session.ts";

export class FilesTreeSession extends SourceViewSession<"tree"> {
  constructor(client: SourceViewsClient, project: string, workspace: string, session: string | undefined, intent: SourceTreeIntent) {
    super(client, project, { kind: "tree", client_id: clientIdentity(), operation_id: crypto.randomUUID(),
      workspace_id: workspace, session_id: session, intent }, frame => ({ rows: frame.rows.length, bytes: new TextEncoder().encode(JSON.stringify(frame)).byteLength }), { rows: 4_000 });
  }

  protected override validateFrame(frame: SourceTreeFrame): void {
    if (!frame.extent.complete || frame.span.end - frame.span.start !== frame.rows.length ||
      frame.rows.some(row => row.kind === "loading")) throw new Error("The file tree returned an incomplete viewport.");
  }

  protected override saveAcceptedIntent(state: SourceTreeView): Promise<void> {
    return saveFilesTreeIntent(state.workspace_id, state.intent_revision, state.intent.disclosures ?? []);
  }

  /** Prefix proofs preserve pages whose coordinates remain unchanged. */
  protected override frameRetention(state: SourceTreeView) {
    const frames = this.presentation().frames.filter(frame => frame.view_id === state.id && frame.intent_revision === state.intent_revision);
    const proofs = [...new Map(frames.flatMap(frame => frame.prefix ? [[frame.prefix.end, frame.prefix] as const] : [])).values()]
      .sort((a, b) => b.end - a.end).slice(0, 32);
    return { query: { retain: proofs }, frames: (next: SourceTreeFrame): SourceTreeFrame[] => {
      const retained = next.retained_prefix;
      if (!retained || !proofs.some(proof => proof.end === retained.end && proof.fingerprint === retained.fingerprint)) return [];
      return frames.filter(frame => frame.span.end <= retained.end &&
        !(frame.span.start >= next.span.start && frame.span.end <= next.span.end)).map(frame => ({ ...frame,
        projection_revision: next.projection_revision, extent: next.extent, retained_prefix: undefined,
        prefix: frame.prefix && frame.prefix.end <= retained.end ? frame.prefix : undefined,
        ancestors: frame.ancestors.map(ancestor => {
          const current = next.ancestors.find(value => value.row.address.root_id === ancestor.row.address.root_id && value.row.address.path === ancestor.row.address.path);
          // A subtree reaching the proof boundary may extend beyond it.
          return current ?? (ancestor.end < retained.end ? ancestor : { ...ancestor, end: UNKNOWN_SUBTREE_END });
        }),
      }));
    } };
  }

  /** Resolves the viewport anchor with context on both sides. */
  async viewport(anchor: { address: SourceTreeAddress; before: number; offset: number }, rows: number, signal: AbortSignal,
    coordinates: "latest" | "presented" = "latest") {
    const leading = treeViewportLeadingRows(anchor.before, rows);
    const frame = await this.frameAt(anchor.address, signal, anchor.before + leading, anchor.offset, coordinates);
    if (frame.target === undefined) throw new Error("The source frame has no resolved target.");
    const start = Math.max(0, frame.target - anchor.before);
    const context = treeLayoutRange(start, start + rows, frame.extent.rows);
    if (context.start < frame.span.start || context.end > frame.span.end) await this.range(start, start + rows, signal);
    return { frame, start };
  }

  retryPreparation(): Promise<SourceTreeView> { return this.retry(); }

  command(command: SourceTreeCommand, base: string | null | undefined = this.presentation().id, signal?: AbortSignal): Promise<SourceTreeView> {
    const release = base ? this.retainPresentation(base) : () => {};
    const operation_id = crypto.randomUUID();
    let attempted = false;
    return this.mutate(state => {
      // A basis applies once, inside the view that presented it.
      const basis = !attempted && command.kind !== "filter" && command.kind !== "review" && this.presentationOf(base, state.id) ? base : undefined;
      attempted = true;
      return this.client.applySourceViewIntent(this.projectId, state.id,
        { kind: "tree", operation_id, expected_intent_revision: state.intent_revision, command, base_presentation_id: basis ?? undefined });
    }, { signal, restartFailed: true, release });
  }

  expand(addresses: SourceTreeAddress[], base = this.presentation().id): Promise<void> {
    return this.command({ kind: "disclose", disclosures: addresses.map(address => ({ address, open: true, recursive: true })) }, base).then(() => {});
  }

  /** Locate and range use the same presentation. */
  async locate(address: SourceTreeAddress, signal?: AbortSignal): Promise<number> {
    return this.readWithRecovery(async state => {
      const presentation = await this.acquirePresentation(state, false, signal);
      const location = await this.client.locateSourceView(this.projectId, state.id,
        { presentation_id: presentation.id, anchor: address }, signal);
      if (location.kind !== "tree") throw new Error("The source location has the wrong kind.");
      if (!location.visible) return -1;
      await this.range(location.index, location.index + 1, signal ?? new AbortController().signal);
      return location.index;
    }, signal);
  }

  /** Read-ahead uses the retained presentation. */
  async prefetch(offset: number, signal: AbortSignal): Promise<boolean> {
    try {
      const previous = this.presentation();
      const total = previous.state?.extent.rows ?? 0;
      // Rows painted by a replaced view do not cover the view that replaced it.
      const current = previous.state?.id === this.state()?.id ? previous.frames : [];
      if (current.some(frame => frame.span.start <= offset && frame.span.end > offset && frame.span.end >= Math.min(offset + 200, total))) return true;
      await this.loadFrame({ offset, limit: 200 }, signal, { foreground: false });
      return true;
    } catch (error) {
      signal.throwIfAborted();
      const code = (error as { code?: string }).code;
      if (code === "source_view_revision_changed" || code === "source_view_preparing" ||
        error instanceof DOMException && error.name === "AbortError") return false;
      throw error;
    }
  }

  async range(start: number, end: number, signal: AbortSignal): Promise<void> {
    this.updateViewport(start, end);
    const context = treeLayoutRange(start, end, Infinity);
    let offset = Math.floor(context.start / 200) * 200;
    for (;;) {
      const state = await this.interested(this.frames.presented() ? this.open() : this.ready(signal), signal);
      const basis = this.basisFor(state);
      if (offset >= Math.min(context.end, basis.extent.rows)) return;
      signal.throwIfAborted();
      try {
        const frame = await this.frame(offset, 200, signal);
        if (frame.span.end <= offset) return;
        offset = frame.span.end;
      } catch (error) {
        signal.throwIfAborted();
        // A frame revision can abort a read while its viewport remains active.
        if (error instanceof DOMException && error.name === "AbortError" && this.attached) { await this.revalidate(signal); continue; }
        const code = (error as { code?: string }).code;
        if (code !== "source_view_preparing" && code !== "source_view_revision_changed") throw error;
        // The next coalesced summary triggers a retry.
        await this.revalidate(signal);
      }
    }
  }
}

type Retained = { session: FilesTreeSession; users: number; lastUsed: number; timer?: ReturnType<typeof setTimeout> };
const clients = new WeakMap<SourceViewsClient, Map<string, Retained>>();

/** Resident stages and remounts share a bounded set of host presentations. */
export function retainFilesTreeSession(client: SourceViewsClient, project: string, workspace: string,
  session: string | undefined, intent: SourceTreeIntent): { view: FilesTreeSession; release(): void } {
  let views = clients.get(client);
  if (!views) { views = new Map(); clients.set(client, views); }
  const key = JSON.stringify([project, workspace, session]);
  let held = views.get(key);
  // A released view (its chat was deleted, or it closed) is never handed out again.
  if (!held || held.session.isReleased()) {
    held = { session: new FilesTreeSession(client, project, workspace, session, intent), users: 0, lastUsed: Date.now() };
    views.set(key, held);
  }
  clearTimeout(held.timer); held.users++; held.lastUsed = Date.now();
  const remove = (key: string, entry: Retained) => {
    if (entry.users || views?.get(key) !== entry) return;
    clearTimeout(entry.timer); views.delete(key);
    void entry.session.close().catch(() => {});
  };
  const idle = [...views].filter(([, entry]) => entry.users === 0).sort((a, b) => a[1].lastUsed - b[1].lastUsed);
  for (const entry of idle) {
    if (views.size <= 8) break;
    remove(...entry);
  }
  const entry = held;
  let retained = true;
  return { view: entry.session, release: () => {
    if (!retained) return;
    retained = false; entry.users--; entry.lastUsed = Date.now();
    if (!entry.users) entry.timer = setTimeout(() => remove(key, entry), 5 * 60_000);
  } };
}
