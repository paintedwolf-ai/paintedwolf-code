import type { Range } from "@codemirror/state";
import { Decoration, EditorView } from "@codemirror/view";
import type { JSX } from "solid-js";
import type { SourceComparisonDigest, SourceComparisonFrame } from "../../api/types.ts";
import type { SourceReaderAccess } from "../../api/source-reader.ts";
import type { SourceComparisonSession } from "../../api/source-comparison-session.ts";
import type { DiffRowSource } from "../../components/source/diff/diff-row-source.ts";
import type { EditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import { SolidBlockViews, SolidBlockWidget, frameBlockViews, type ReactiveScope } from "../../components/source/editor/solid-block-widget.ts";
import { ReaderEditor } from "../../components/source/reader/source-reader-editor.ts";
import { MAIN_SECTION, type ReaderDocument, type ReaderSlot } from "../../components/source/reader/source-reader-document.ts";
import { comparisonWindow } from "../../components/source/reader/source-reader-window.ts";
import { isScrollActive, subscribeScrollActivity } from "../../platform/scrolling/scroll-activity.ts";
import {
  PAGE_HEADER_RESERVE_PX,
  PAGE_SECTION,
  SECTION_HEADER_RESERVE_PX,
  diffsDocumentSlots,
  sectionIsTitleOnly,
  sectionPlaceholder,
} from "./diffs-document.ts";

export type ReaderFactsHandlers = {
  show: (editor: ReaderEditor, line: number, anchor: HTMLElement, enter: boolean) => void;
  leave: () => void;
};

/** Codes indicating the underlying baseline shifted and requires a fresh presentation. */
const MOVED_CODES = new Set(["source_view_not_found", "source_view_revision_changed", "source_view_preparing"]);

/** Outcome of an aborted or failed read. */
export type ReadOutcome = "superseded" | "moved" | "failed";

/** Distinguishes expected read cancellations and baseline shifts from unexpected failures. */
export function readOutcome(cause: unknown, closed: boolean, token: number, current: number | undefined): ReadOutcome {
  if (closed || token !== current) return "superseded";
  if (cause instanceof DOMException && cause.name === "AbortError") return "superseded";
  const code = (cause as { code?: string } | null)?.code;
  return code !== undefined && MOVED_CODES.has(code) ? "moved" : "failed";
}

/** Maximum sections holding loaded row state. */
const RETAINED_SECTIONS = 12;
/** Maximum retained heading block widgets. */
const RETAINED_HEADINGS = 24;
/** A held rebuild publishes after this long even if the gesture continues. */
const PUBLISH_HOLD_MS = 400;
/** Past this, counted from the first rebuild a gesture held, rebuilds stop waiting for it. */
const GESTURE_HOLD_LIMIT_MS = 1500;

export type DiffsSectionInput = {
  key: string;
  source: DiffRowSource;
  digest: SourceComparisonDigest | undefined;
};

export type DiffsReaderArgs = {
  scope: ReactiveScope;
  sections: () => readonly DiffsSectionInput[];
  prefs: () => EditorDisplayPrefs;
  open: (key: string) => boolean;
  revision: (key: string) => number | null;
  /** Notifies when a section begins or finishes a stable reload. */
  onReloading?: (key: string, reloading: boolean) => void;
  pageHeading: () => JSX.Element;
  sectionHeading: (key: string) => JSX.Element;
  onError: (cause: unknown) => void;
};

type SectionRuntime = {
  source?: DiffRowSource;
  digest?: SourceComparisonDigest;
  revision?: number | null;
  access?: SourceReaderAccess;
  session?: SourceComparisonSession;
  detach?: () => void;
  frames: readonly SourceComparisonFrame[];
  window: readonly ReaderSlot[];
  /** Reserved slot while rows are unread. */
  placeholder?: ReaderSlot;
  loading: boolean;
  /** Whether the section is reloading a new revision while retaining current rows. */
  reloading: boolean;
  /** The latest read asked for while another was in flight. */
  queued?: { offset: number; unfold?: ReaderSlot };
  generation: number;
  /** Recency clock for cache eviction. */
  touched: number;
};

/** Single virtualized reader editor rendering all file diff sections. */
export function createDiffsReader(args: DiffsReaderArgs) {
  const runtimes = new Map<string, SectionRuntime>();
  /** Keys of sections with loaded rows. */
  const loaded = new Set<string>();
  const headings = new SolidBlockViews(RETAINED_HEADINGS);
  const controller = new AbortController();
  let editor: ReaderEditor | undefined;
  let publishing = false;
  let pending = false;
  let heldSince: number | undefined;
  let unwatchScroll: (() => void) | undefined;
  let holdTimer: ReturnType<typeof setTimeout> | undefined;
  let clock = 0;
  let factsSection = MAIN_SECTION;
  let displayPrefs = args.prefs();

  const lineHeightPx = (): number => displayPrefs.fontSize * displayPrefs.lineHeight;

  function sectionWindow(key: string, frames: readonly SourceComparisonFrame[], session: SourceComparisonSession): ReaderSlot[] {
    return comparisonWindow(frames, session.state()?.comparison?.summary?.rows ?? 0, lineHeightPx())
      .map((slot) => ({ ...slot, section: key }));
  }

  /** Rebuilt only when the sections array changes. */
  let index: { of: readonly DiffsSectionInput[]; byKey: Map<string, DiffsSectionInput> } | undefined;
  function input(key: string): DiffsSectionInput | undefined {
    const of = args.sections();
    if (index?.of !== of) index = { of, byKey: new Map(of.map((value) => [value.key, value])) };
    return index.byKey.get(key);
  }

  function runtime(key: string): SectionRuntime {
    let held = runtimes.get(key);
    if (!held) {
      const section = input(key);
      held = {
        source: section?.source,
        digest: section?.digest,
        revision: section ? args.revision(key) : null,
        frames: [],
        window: [],
        loading: false,
        reloading: false,
        generation: 0,
        touched: ++clock,
      };
      runtimes.set(key, held);
    }
    return held;
  }

  async function closeSession(held: SectionRuntime): Promise<void> {
    held.detach?.();
    held.detach = undefined;
    const session = held.session;
    held.session = undefined;
    if (session) await session.close().catch(() => {});
  }

  function endReload(key: string, held: SectionRuntime): void {
    if (!held.reloading) return;
    held.reloading = false;
    args.onReloading?.(key, false);
  }

  /** Drops a section's rows and session; any read in flight for them is superseded. */
  async function clearRows(key: string, held: SectionRuntime): Promise<void> {
    held.generation += 1;
    held.frames = [];
    held.window = [];
    endReload(key, held);
    loaded.delete(key);
    await closeSession(held);
  }

  async function release(key: string): Promise<void> {
    const held = runtimes.get(key);
    if (held) await clearRows(key, held);
  }

  /** Keeps a section's rows on screen while a fresh read replaces them. */
  function beginReload(key: string, held: SectionRuntime): void {
    held.generation += 1;
    if (!held.reloading) {
      held.reloading = true;
      args.onReloading?.(key, true);
    }
    void closeSession(held);
    if (!held.loading) void load(key, 0);
  }

  /** Evicts least recently read sections exceeding retention budget. */
  function trim(): void {
    if (loaded.size <= RETAINED_SECTIONS) return;
    const held = [...loaded].flatMap((key) => {
      const state = runtimes.get(key);
      return state && !state.loading ? [[key, state.touched] as const] : [];
    });
    if (held.length <= RETAINED_SECTIONS) return;
    held.sort((left, right) => left[1] - right[1]);
    for (const [key] of held.slice(0, held.length - RETAINED_SECTIONS)) void release(key);
  }

  /** Batches document rebuilds to microtasks and defers them while scrolling is active. */
  function publish(reset = false): void {
    if (publishing || controller.signal.aborted) return;
    publishing = true;
    queueMicrotask(() => {
      publishing = false;
      if (controller.signal.aborted) return;
      if (!reset && scrolling()) { hold(); return; }
      publishNow(reset);
    });
  }

  /** Defers document updates until scrolling settles or the hold timeout expires. */
  function hold(): void {
    pending = true;
    if (holdTimer !== undefined) return;
    holdTimer = setTimeout(() => { holdTimer = undefined; flush(); }, PUBLISH_HOLD_MS);
  }

  /** Whether an active gesture still holds rebuilds; the first call it holds starts the clock. */
  function scrolling(): boolean {
    const scroller = editor?.view.scrollDOM;
    if (!scroller || !isScrollActive(scroller)) return false;
    if (heldSince === undefined) heldSince = performance.now();
    return performance.now() - heldSince < GESTURE_HOLD_LIMIT_MS;
  }

  function flush(): void {
    heldSince = undefined;
    if (holdTimer !== undefined) { clearTimeout(holdTimer); holdTimer = undefined; }
    if (!pending || controller.signal.aborted) return;
    pending = false;
    publishNow(false);
  }

  function publishNow(reset = false): void {
    if (!editor) return;
    const currentSections = args.sections();
    const activeKeys = new Set(currentSections.map((s) => s.key));
    for (const key of [...runtimes.keys()]) {
      if (!activeKeys.has(key)) {
        void release(key);
        runtimes.delete(key);
      }
    }
    const slots = diffsDocumentSlots(currentSections.map((input) => {
      const rev = args.revision(input.key);
      const held = runtimes.get(input.key);
      if (held) {
        if (held.source !== input.source || held.revision !== rev) {
          held.source = input.source;
          held.digest = input.digest;
          held.revision = rev;
          if (sectionIsTitleOnly({ digest: input.digest, change: input.source.change(rev), revision: rev })) {
            void clearRows(input.key, held);
          } else if (held.window.length) {
            beginReload(input.key, held);
          } else {
            // A fresh placeholder asks the viewport to read the new source.
            held.placeholder = undefined;
            void clearRows(input.key, held);
          }
        } else if (held.digest !== input.digest) {
          held.digest = input.digest;
          if (!held.window.length) held.placeholder = undefined;
        }
      }
      const state = {
        key: input.key,
        digest: input.digest,
        change: input.source.change(rev),
        open: args.open(input.key),
        window: held?.window ?? [],
        reloading: held?.reloading ?? false,
        revision: rev,
      };
      if (!state.open || state.window.length || sectionIsTitleOnly(state)) return state;
      const placeholder = sectionPlaceholder(input.key, input.digest, held?.placeholder);
      if (held) held.placeholder = placeholder;
      return { ...state, placeholder };
    }));
    editor.setRows(slots, reset);
  }

  /** Reads comparison frames around an offset, unfolding folded runs when requested. */
  async function load(key: string, offset: number, unfold?: ReaderSlot): Promise<void> {
    const section = input(key);
    if (!section || !args.open(key) || controller.signal.aborted) return;
    const revision = args.revision(key);
    if (sectionIsTitleOnly({ digest: section.digest, change: section.source.change(revision), revision })) {
      const held = runtimes.get(key);
      if (held) void clearRows(key, held);
      return;
    }
    const held = runtime(key);
    if (held.loading) {
      held.queued = { offset, unfold };
      return;
    }
    held.loading = true;
    const token = ++held.generation;
    // A reset, release, or new source replaces the runtime or bumps its generation.
    const current = () => !controller.signal.aborted && runtimes.get(key) === held && held.generation === token;
    let failed = false;
    try {
      const access = section.source.access(revision);
      if (!access) return;
      if (held.access !== access) {
        held.access = access;
        await closeSession(held);
        if (!current()) return;
      }
      if (!held.session) {
        const session = await access.presentation({ mode: "changes" }, controller.signal);
        if (!current()) { await session.close().catch(() => {}); return; }
        held.session = session;
        held.detach = session.attach();
        await session.ready(controller.signal);
        if (!current()) return;
      }
      const session = held.session;
      if (!session) return;
      if (unfold) {
        await session.unfold(unfold.index, unfold.end);
        if (!current()) return;
        // Unfolding renumbers the projection; update offset from the new frame boundary.
        const frame = await session.frameAt({ row: unfold.index }, controller.signal);
        if (!current()) return;
        offset = frame.span.start;
      }
      const extent = session.state()?.extent.rows ?? 0;
      const centre = Math.max(0, Math.floor(offset / 200) * 200);
      const wanted = [centre, centre - 200, centre + 200].filter((value) => value >= 0 && value < extent);
      const frames = await Promise.all(wanted.map((value) => session.frame(value, 200, controller.signal)));
      if (!current()) return;
      held.frames = frames;
      held.touched = ++clock;
      endReload(key, held);
      held.window = sectionWindow(key, frames, session);
      loaded.add(key);
      trim();
      publish();
    } catch (cause) {
      const outcome = readOutcome(cause, controller.signal.aborted, token, runtimes.get(key) === held ? held.generation : undefined);
      if (outcome === "superseded") return;
      // A moved baseline reads again below; a failure waits for the reader.
      failed = outcome === "failed";
      void clearRows(key, held).then(() => publish());
      if (failed) args.onError(cause);
    } finally {
      held.loading = false;
      const queued = held.queued;
      held.queued = undefined;
      if (runtimes.get(key) === held && !controller.signal.aborted) {
        // A superseded read hands a section still waiting on rows to a fresh read.
        if (held.generation !== token) {
          if (!failed && (held.reloading || queued || !held.window.length)) void load(key, queued?.offset ?? 0);
        } else if (queued) {
          void load(key, queued.offset, queued.unfold);
        }
      }
    }
  }

  function marks(document: ReaderDocument): Range<Decoration>[] {
    const out: Range<Decoration>[] = [];
    for (const entry of document.entries) {
      if (!entry.slot.header) continue;
      const key = entry.section;
      const widget = key === PAGE_SECTION
        ? new SolidBlockWidget(PAGE_SECTION, headings, args.scope, args.pageHeading, PAGE_HEADER_RESERVE_PX)
        : new SolidBlockWidget(key, headings, args.scope, () => args.sectionHeading(key), SECTION_HEADER_RESERVE_PX);
      out.push(Decoration.replace({ widget, block: true }).range(entry.from, entry.to));
    }
    return out;
  }

  return {
    /** Active section key under the fact cursor. */
    get factsSection(): string { return factsSection; },
    mount(parent: HTMLElement, facts: ReaderFactsHandlers): ReaderEditor {
      editor = new ReaderEditor({
        parent,
        surface: "diffs",
        prefs: displayPrefs,
        scrollPastEnd: false,
        changes: () => true,
        fullSide: () => "after",
        access: (section) => input(section)?.source.access(args.revision(section)),
        marks,
        extensions: frameBlockViews(headings),
        load: (slot, offset) => {
          void load(slot.section ?? MAIN_SECTION, offset ?? slot.index, slot.pending ? undefined : slot);
        },
        error: args.onError,
        facts: (target, line, anchor, enter) => {
          factsSection = target.document.at(target.view.state.doc.line(line).from)?.section ?? MAIN_SECTION;
          facts.show(target, line, anchor, enter);
        },
        hideFacts: facts.leave,
      });
      unwatchScroll = subscribeScrollActivity(editor.view.scrollDOM, (phase) => { if (phase === "settle") flush(); });
      publishNow(true);
      return editor;
    },
    /** Applies display prefs and resizes loaded windows to the new line height. */
    prefs(next: EditorDisplayPrefs): void {
      const resized = next.fontSize * next.lineHeight !== lineHeightPx();
      displayPrefs = next;
      editor?.prefs(next);
      if (!resized) return;
      for (const [key, held] of runtimes) {
        if (held.frames.length && held.session && !held.reloading) held.window = sectionWindow(key, held.frames, held.session);
      }
      publish();
    },
    refresh(): void { publish(); },
    reset(): void {
      if (holdTimer !== undefined) { clearTimeout(holdTimer); holdTimer = undefined; }
      pending = false;
      heldSince = undefined;
      for (const [key, held] of runtimes.entries()) void clearRows(key, held);
      runtimes.clear();
      loaded.clear();
      factsSection = MAIN_SECTION;
      if (editor) {
        editor.view.scrollDOM.scrollTop = 0;
        publishNow(true);
      }
    },
    /** Reloads a section, retaining loaded rows while the new revision presentation is prepared. */
    reload(key: string): void {
      const held = runtimes.get(key);
      if (held && held.window.length) {
        beginReload(key, held);
        publish();
      } else {
        void release(key).then(() => publish());
      }
    },
    revealSection(sectionKey: string): boolean {
      if (!editor) return false;
      const span = editor.document.sectionSpan(sectionKey);
      if (!span) return false;
      editor.view.dispatch({
        selection: { anchor: span.from },
        effects: [EditorView.scrollIntoView(span.from, { y: "start" })],
      });
      return true;
    },
    /** Section currently near the top of the viewport. */
    visibleSectionKey(): string | undefined {
      if (!editor) return undefined;
      const scrollDOM = editor.view.scrollDOM;
      const targetY = scrollDOM.scrollTop + 30;
      const block = editor.view.lineBlockAtHeight(targetY);
      const entry = editor.document.at(block.from);
      return entry?.section;
    },
    destroy(): void {
      controller.abort();
      unwatchScroll?.();
      unwatchScroll = undefined;
      if (holdTimer !== undefined) { clearTimeout(holdTimer); holdTimer = undefined; }
      for (const key of [...runtimes.keys()]) void release(key);
      runtimes.clear();
      editor?.destroy();
      editor = undefined;
      headings.destroy();
    },
  };
}
