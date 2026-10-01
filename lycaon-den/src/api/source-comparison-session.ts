import type { SourceViewsClient, SourceViewLocationTarget } from "./source-views-client.ts";
import type {
  SourceComparisonFrame, SourceComparisonIntent, SourceComparisonLocation,
  SourceComparisonSearchPage, SourceComparisonSelector, SourceComparisonView, SourceReaderRow,
  SourceComparisonSummary, SourceComparisonViewCreate,
} from "./types.ts";
import { clientIdentity } from "../platform/connection/client-identity.ts";
import { SourceViewSession } from "../ui/paged-view/source-view-session.ts";

export type ComparisonMode = SourceComparisonIntent["mode"];
export type ComparisonSide = "before" | "after";
export type ComparisonBoundary = { row: number; offset: number; side: ComparisonSide };

/** Presentations share immutable content and retain independent disclosure intent. */
export class SourceComparisonSession extends SourceViewSession<"comparison"> {
  private expected?: SourceComparisonSummary;
  private recovering?: Promise<SourceComparisonView>;
  constructor(client: SourceViewsClient, project: string, readonly source: SourceComparisonSelector,
    readonly sessionId?: string, intent: SourceComparisonIntent = { mode: "changes" },
    initial?: SourceComparisonView, retained?: { id: string; summary?: SourceComparisonSummary },
    private readonly resolveSource?: () => Promise<SourceComparisonSelector>) {
    super(client, project, { kind: "comparison", operation_id: crypto.randomUUID(), client_id: clientIdentity(),
      session_id: sessionId, source: retained ? { kind: "retained", view_id: retained.id, comparison: "before" } : source, intent }, comparisonFrameCost);
    this.expected = initial?.comparison?.summary ?? retained?.summary;
    if (initial) this.install(initial);
  }

  override async ready(signal?: AbortSignal): Promise<SourceComparisonView> {
    try { return await super.ready(signal); }
    catch (error) {
      signal?.throwIfAborted();
      const code = (error as { code?: string }).code ?? this.state()?.failure?.code;
      if ((code !== "expired" && code !== "source_view_not_found" && code !== "source_workspace_mismatch") || this.source.kind === "retained" && !this.resolveSource) throw error;
      this.recovering ??= this.retry().finally(() => { this.recovering = undefined; });
      await this.interested(this.recovering, signal);
      return super.ready(signal);
    }
  }

  async fork(intent: SourceComparisonIntent = { mode: "changes" }, signal?: AbortSignal): Promise<SourceComparisonSession> {
    await this.refresh();
    const state = await this.ready(signal);
    return new SourceComparisonSession(this.client, this.projectId, this.source, this.sessionId, intent,
      undefined, { id: state.id, summary: state.comparison?.summary }, this.resolveSource);
  }

  protected override async replacementRequest(previous?: SourceComparisonView): Promise<SourceComparisonViewCreate> {
    return { ...await super.replacementRequest(previous), source: this.resolveSource ? await this.resolveSource() : this.source };
  }

  protected override validateState(state: SourceComparisonView): void {
    const summary = state.comparison?.summary;
    if (state.state !== "ready" || !summary) return;
    if (this.expected && !sameComparisonEndpoints(this.expected, summary)) throw new Error("The comparison changed. Reopen it to continue.");
    this.expected = summary;
  }

  async mode(mode: ComparisonMode): Promise<SourceComparisonView> {
    await this.ready();
    return this.update(intent => ({ ...intent, mode }));
  }

  async unfold(start: number, end: number): Promise<SourceComparisonView> {
    await this.ready();
    return this.update(intent => {
      const ranges = [...intent.expanded ?? [], { start, end }].sort((a, b) => a.start - b.start);
      const expanded: { start: number; end: number }[] = [];
      for (const range of ranges) {
        const previous = expanded.at(-1);
        if (previous && range.start <= previous.end) previous.end = Math.max(previous.end, range.end);
        else expanded.push({ ...range });
      }
      return { ...intent, expanded };
    });
  }

  async locate(query: SourceViewLocationTarget, signal?: AbortSignal): Promise<SourceComparisonLocation> {
    await this.ready(signal);
    const location = await this.readWithRecovery(async current => { const presentation = await this.acquirePresentation(current, false, signal); return this.client.locateSourceView(this.projectId, current.id, { ...query, presentation_id: presentation.id }, signal); }, signal);
    if (location.kind !== "comparison" || location.view_id !== this.state()?.id) throw new Error("The source location has the wrong identity.");
    if (location.projection_revision !== this.state()?.projection_revision) await this.refresh();
    return location;
  }

  async reveal(row: number, signal?: AbortSignal): Promise<SourceComparisonFrame> {
    await this.ready(signal);
    let frame = await this.frameAt({ row }, signal);
    const folded = frame.rows.find(candidate => candidate.kind === "gap" && candidate.index <= row && candidate.end > row);
    if (folded) {
      await this.unfold(folded.index, folded.end);
      signal?.throwIfAborted();
      frame = await this.frameAt({ row }, signal);
    }
    return frame;
  }

  async search(query: string, cursor: string | undefined, caseSensitive: boolean,
    signal?: AbortSignal): Promise<SourceComparisonSearchPage> {
    await this.ready(signal);
    const page = await this.readWithRecovery(async state => { const presentation = await this.acquirePresentation(state, false, signal); return this.client.searchSourceView(this.projectId, state.id,
      { presentation_id: presentation.id, q: query, cursor, case_sensitive: caseSensitive, limit: 200 }, signal); }, signal);
    if (page.kind !== "comparison" || page.view_id !== this.state()?.id) throw new Error("The source search has the wrong identity.");
    return page;
  }

  /** Copy traverses a separate side presentation without disturbing the visible view. */
  async text(side: ComparisonSide, start?: ComparisonBoundary, end?: ComparisonBoundary,
    signal?: AbortSignal): Promise<string> {
    const state = await this.ready(signal);
    if (!state.comparison?.summary?.rows) return "";
    const copy = await this.fork({ mode: side }, signal);
    const detach = copy.attach();
    try {
      await copy.ready(signal);
      const parts: string[] = [];
      let units = 0;
      let frame = start ? await copy.frameAt({ row: start.row }, signal) : await copy.frame(0, 200, signal);
      for (;;) {
        signal?.throwIfAborted();
        for (const row of frame.rows) {
          if (start && row.index < start.row) continue;
          if (end && row.index > end.row) return parts.join("");
          const from = start?.row === row.index ? start.offset : 0;
          const to = end?.row === row.index ? end.offset : row.text.length;
          const part = row.text.slice(from, to);
          units += part.length;
          if (units > 8 * 1024 * 1024) throw new Error("The selection is too large to copy. Select a smaller range.");
          parts.push(part);
        }
        if (frame.span.end >= frame.extent.rows) return parts.join("");
        if (frame.span.end <= frame.span.start) throw new Error("The source range did not advance.");
        frame = await copy.frame(frame.span.end, 200, signal);
      }
    } finally { detach(); await copy.close(); }
  }

  private update(change: (intent: SourceComparisonIntent) => SourceComparisonIntent): Promise<SourceComparisonView> {
    const operation_id = crypto.randomUUID();
    return this.mutate(state => this.client.applySourceViewIntent(this.projectId, state.id,
      { kind: "comparison", operation_id, expected_intent_revision: state.intent_revision, intent: change(state.intent) }));
  }
}

function sameComparisonEndpoints(left: SourceComparisonSummary, right: SourceComparisonSummary): boolean {
  return (["before", "after"] as const).every(side => left[side].sha256 === right[side].sha256
    && left[side].availability === right[side].availability && left[side].path === right[side].path);
}

function rowBytes(row: SourceReaderRow): number {
  return row.text.length * 2 + 256 + (row.changed.length + (row.syntax?.length ?? 0)
    + (row.secret_screen?.spans?.length ?? 0)) * 64 + (row.contributors?.length ?? 0) * 256;
}
export function comparisonFrameCost(frame: SourceComparisonFrame): { rows: number; bytes: number } {
  return { rows: frame.rows.length, bytes: 512 + frame.rows.reduce((sum, row) => sum + rowBytes(row) + (row.peer ? rowBytes(row.peer) : 0), 0) };
}
