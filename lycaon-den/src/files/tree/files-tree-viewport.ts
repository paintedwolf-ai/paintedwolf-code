import type { FilesTreeSession } from "./files-tree-paged-session.ts";
import type { PagedTreeModel } from "./files-tree-paged-model.ts";

const PAGE_ROWS = 200;
const BUFFER_PAGES = 3;
/** A failed speculative read waits this long before read-ahead resumes. */
const READ_AHEAD_RETRY_MS = 5_000;

export function treeViewportLeadingRows(before: number, visibleRows: number): number {
  return Math.min(PAGE_ROWS - 1 - before, Math.max(0, Math.floor((PAGE_ROWS - visibleRows) / 2)));
}

/** A known row below a cache gap can anchor the entire visible window. */
export function treeViewportAnchor(model: PagedTreeModel, start: number) {
  const sourceStart = model.sourceIndex(start);
  for (let index = start; index < Math.min(model.length, start + PAGE_ROWS); index++) {
    const row = model.rowAt(index);
    if (row?.kind !== "entry") continue;
    return { address: { root_id: row.entry.rootId, path: row.entry.path }, before: model.sourceIndex(index) - sourceStart, offset: 0 };
  }
  const ancestor = model.chainAt(start).at(-1);
  return ancestor ? { address: { root_id: ancestor.entry.rootId, path: ancestor.entry.path }, before: 0,
    offset: sourceStart - model.sourceIndex(ancestor.displayIndex) } : undefined;
}

/** Keeps overlapping requests alive as the viewport moves through its read-ahead buffer.
 * Speculative reads never surface failures: a foreground read of the same rows reports its own. */
export class FilesTreeViewport {
  private readonly pages = new Map<number, { controller: AbortController; loaded: boolean }>();
  private running?: AbortController;
  private blockedRevision?: string;
  private resume?: ReturnType<typeof setTimeout>;
  private revision = "";
  private previousStart = 0;
  private direction = 1;

  constructor(private readonly view: Pick<FilesTreeSession, "prefetch" | "updateViewport" | "clearViewport">) {}

  seek(start: number, end: number, total: number, revision: string): void {
    if (revision !== this.revision) { this.clear(); this.revision = revision; }
    if (start !== this.previousStart) this.direction = start > this.previousStart ? 1 : -1;
    this.previousStart = start;
    this.view.updateViewport(start, end, this.direction);
    const first = Math.floor(Math.max(0, start) / PAGE_ROWS);
    const last = Math.floor(Math.max(start, end - 1) / PAGE_ROWS);
    const count = Math.ceil(total / PAGE_ROWS);
    const wanted = new Set<number>();
    // Visible pages enter before speculative pages, which cannot delay a new seek.
    for (let page = first; page <= last && page < count; page++) wanted.add(page * PAGE_ROWS);
    for (let distance = 1; distance <= BUFFER_PAGES; distance++) {
      const forward = last + distance, backward = first - distance;
      for (const page of this.direction > 0 ? [forward, backward] : [backward, forward]) {
        if (page >= 0 && page < count) wanted.add(page * PAGE_ROWS);
      }
    }
    for (const [offset, page] of this.pages) {
      if (!wanted.has(offset)) { page.controller.abort(); this.pages.delete(offset); }
    }
    const ordered = [...wanted].map(offset => [offset,
      this.pages.get(offset) ?? { controller: new AbortController(), loaded: false }] as const);
    this.pages.clear();
    for (const [offset, page] of ordered) this.pages.set(offset, page);
    this.pump();
  }

  private pump(): void {
    if (this.running || this.blockedRevision === this.revision) return;
    const next = [...this.pages].find(([, page]) => !page.loaded);
    if (!next) return;
    const [offset, page] = next;
    const revision = this.revision;
    this.running = page.controller;
    // One speculative read leaves a request slot for foreground navigation.
    void this.view.prefetch(offset, page.controller.signal).then(loaded => {
      if (page.controller.signal.aborted) return;
      page.loaded = loaded;
      if (!loaded) this.blockedRevision = revision;
    }).catch(() => {
      if (page.controller.signal.aborted) return;
      this.blockedRevision = revision;
      clearTimeout(this.resume);
      this.resume = setTimeout(() => {
        if (this.blockedRevision === revision) this.blockedRevision = undefined;
        this.pump();
      }, READ_AHEAD_RETRY_MS);
    }).finally(() => {
      if (this.running === page.controller) this.running = undefined;
      this.pump();
    });
  }

  clear(): void {
    this.view.clearViewport();
    clearTimeout(this.resume);
    for (const page of this.pages.values()) page.controller.abort();
    this.blockedRevision = undefined;
    this.pages.clear();
  }
}
