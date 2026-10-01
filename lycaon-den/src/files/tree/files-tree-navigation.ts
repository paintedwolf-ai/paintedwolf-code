import { editorTreeVisibleLevelsPref } from "../../settings/editor/editor-prefs.ts";
import type { SourceTreeAddress } from "../../api/types.ts";
import type { FilesTreeSession } from "./files-tree-paged-session.ts";
import type { PagedTreeModel } from "./files-tree-paged-model.ts";
import { FILES_TREE_ROW_HEIGHT_PX as H } from "./project-files-tree-flat.ts";
import { treeViewportAnchor, treeViewportLeadingRows } from "./files-tree-viewport.ts";
import { scrollTopToAlignRowAtStart, scrollTopToAlignRowCentered, scrollTopToRevealRow } from "./files-tree-virtual-scroll.ts";
import { stickyStackFromIndex, treeLayoutRange } from "./files-tree-sticky.ts";

export type TreePresentation = ReturnType<FilesTreeSession["presentation"]>;
export type TreeNavigationViewport = { presentation: TreePresentation; offset: number };

/** A reveal target can be outside the tree: filtered, out of review scope, or deleted. */
export class FilesTreePathAbsentError extends Error {
  constructor(path: string) { super(`This path is not in the current tree: ${path}`); }
}

type Options = {
  view: FilesTreeSession;
  model(presentation: TreePresentation): PagedTreeModel;
  height(): number;
};

function revealOffset(model: PagedTreeModel, index: number, align: "nearest" | "start" | "center", start: number, viewportHeight: number): number {
  const common = { index, rowHeight: H, viewportHeight, contentHeight: model.length * H,
    stickyHeightAt: (offset: number) => stickyStackFromIndex(model, offset, viewportHeight, editorTreeVisibleLevelsPref()).height };
  return (align === "start" ? scrollTopToAlignRowAtStart(common)
    : align === "center" ? scrollTopToAlignRowCentered(common)
    : scrollTopToRevealRow({ ...common, scrollTop: start })) ?? start;
}

type RevealOptions = Options & {
  address: SourceTreeAddress;
  isDir: boolean;
  align: "nearest" | "start" | "center";
  previous: TreeNavigationViewport;
  signal: AbortSignal;
};

/** An already expanded path needs no command, new presentation, or anchor rebase. */
async function retainedNavigation(options: RevealOptions) {
  const { view, previous, signal, address, isDir } = options;
  const sameCoordinates = () => previous.presentation.id && view.presentation().id === previous.presentation.id &&
    view.state()?.intent_revision === previous.presentation.state?.intent_revision;
  if (!sameCoordinates() || !previous.presentation.frames.length) return;
  let model = options.model(view.presentation());
  if (model.indexOfEntry(address.root_id, address.path, isDir) < 0) {
    const parts = address.path === "." ? [] : address.path.split("/");
    const parents = [".", ...parts.slice(0, -1).map((_part, index) => parts.slice(0, index + 1).join("/"))];
    if (parents.some(path => {
      const parent = model.rowAt(model.indexOfEntry(address.root_id, path, true));
      return parent?.kind === "entry" && !parent.entry.expanded;
    })) return;
    const before = treeViewportLeadingRows(0, Math.ceil(options.height() / H));
    await view.frameAt(address, signal, before, 0, "presented");
    if (!sameCoordinates()) return;
    model = options.model(view.presentation());
  }
  const index = model.indexOfEntry(address.root_id, address.path, isDir);
  const row = model.rowAt(index);
  if (row?.kind !== "entry" || isDir && !row.entry.expanded) return;
  const navigation = new FilesTreeNavigation(options);
  try {
    await navigation.prepare(previous.offset, signal);
    return { navigation, target: revealOffset(model, index, options.align, navigation.viewport().offset, options.height()) };
  } catch (error) { navigation.dispose(); throw error; }
}

/** One reveal reads and paints only in its retained presentation's coordinates. */
export class FilesTreeNavigation {
  private readonly id: string;
  private readonly release: () => void;
  private prepared: TreeNavigationViewport;

  constructor(private readonly options: Options) {
    const presentation = options.view.presentation();
    if (!presentation.id) throw new Error("The file tree has no presentation.");
    this.id = presentation.id;
    this.release = options.view.retainPresentation(this.id);
    this.prepared = { presentation, offset: 0 };
  }

  dispose(): void { this.release(); }
  viewport(): TreeNavigationViewport { return this.prepared; }

  private presentation(signal: AbortSignal): TreePresentation {
    signal.throwIfAborted();
    const presentation = this.options.view.presentation();
    if (presentation.id !== this.id) throw new DOMException("The tree navigation was superseded.", "AbortError");
    return presentation;
  }

  /** Cached destinations publish synchronously; missing context retains the current viewport. */
  prepare(offset: number, signal: AbortSignal): void | Promise<void> {
    const presentation = this.presentation(signal);
    const model = this.options.model(presentation);
    const height = this.options.height();
    const top = Math.max(0, Math.min(Math.max(0, model.length * H - height), offset));
    const first = Math.floor(top / H);
    const last = Math.min(model.length, Math.ceil((top + height) / H));
    const context = treeLayoutRange(first, last, model.length);
    let covered = true;
    for (let index = context.start; index < context.end; index++) if (!model.rowAt(index)) { covered = false; break; }
    if (covered) { this.prepared = { presentation, offset: top }; return; }
    return this.options.view.range(model.sourceIndex(context.start), model.sourceIndex(context.end), signal).then(() => {
      const next = this.presentation(signal);
      const rows = this.options.model(next);
      for (let index = first; index < last; index++) {
        if (!rows.rowAt(index)) throw new Error("The file tree returned an incomplete navigation viewport.");
      }
      this.prepared = { presentation: next, offset: top };
    });
  }
}

export async function prepareFilesTreeNavigation(options: RevealOptions): Promise<{ navigation: FilesTreeNavigation; target: number }> {
  const { view, signal, address, isDir, previous } = options;
  const retained = await retainedNavigation(options);
  if (retained) return retained;
  const align = options.align === "nearest" ? "center" : options.align;
  const previousModel = options.model(previous.presentation);
  const anchor = treeViewportAnchor(previousModel, Math.floor(previous.offset / H));
  if (isDir) {
    const parts = address.path === "." ? [] : address.path.split("/");
    const paths = [".", ...parts.map((_, i) => parts.slice(0, i + 1).join("/"))];
    const disclosures = paths.map(p => ({ address: { root_id: address.root_id, path: p }, open: true, recursive: false }));
    await view.command({ kind: "disclose", disclosures }, previous.presentation.id, signal);
  } else {
    await view.command({ kind: "reveal", address }, previous.presentation.id, signal);
  }
  const before = treeViewportLeadingRows(0, Math.ceil(options.height() / H));
  await view.frameAt(address, signal, before);
  signal.throwIfAborted();
  const destination = options.model(view.presentation());
  const index = destination.indexOfEntry(address.root_id, address.path, isDir);
  if (index < 0) throw new FilesTreePathAbsentError(address.path);
  const navigation = new FilesTreeNavigation(options);
  try {
    let start = previous.offset;
    if (anchor) {
      const anchorIdx = destination.indexOfEntry(anchor.address.root_id, anchor.address.path, false);
      const entryIdx = anchorIdx >= 0 ? anchorIdx : destination.indexOfEntry(anchor.address.root_id, anchor.address.path, true);
      const row = entryIdx >= 0 ? destination.rowAt(entryIdx) : undefined;
      if (anchor.offset === 0 && entryIdx >= 0 && row?.kind === "entry") {
        const sourceStart = Math.max(0, destination.sourceIndex(entryIdx) - anchor.before);
        start = destination.displayIndex(sourceStart) * H + previous.offset % H;
      } else {
        const resolved = await view.viewport(anchor, Math.ceil(options.height() / H), signal, "presented");
        start = options.model(view.presentation()).displayIndex(resolved.start) * H + previous.offset % H;
      }
    }
    await navigation.prepare(start, signal);
    return { navigation, target: revealOffset(destination, index, align, navigation.viewport().offset, options.height()) };
  } catch (error) { navigation.dispose(); throw error; }
}
