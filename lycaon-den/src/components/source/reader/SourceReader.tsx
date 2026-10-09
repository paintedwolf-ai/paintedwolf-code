import type { DenReaderViewState } from "../../../../shared/app-state-types.ts";
import { Show, createEffect, createMemo, createSignal, onCleanup, untrack, type JSX } from "solid-js";
import type { SourceComparisonFrame, SourceReaderMatch, SourceContributor } from "../../../api/types.ts";
import type { SourceReaderAccess } from "../../../api/source-reader.ts";
import { useResidentInteractive } from "../../../ui/resident-presence-context.tsx";
import { useResidentLive } from "../../../ui/resident-activity.ts";
import { findController } from "../../../find/find-controller.ts";
import { openGotoLine } from "../../../find/goto-line-controller.ts";
import { observeSurfaceFailure } from "../../../notices/surface-failure.ts";
import { bindFindableView } from "../../../find/use-findable-view.ts";
import { bindReaderFind } from "./source-reader-find.ts";
import { createReaderFacts } from "./source-reader-facts.tsx";
import { ReaderSplit } from "./source-reader-split.ts";
import { ReaderEditor, type ReaderSelection } from "./source-reader-editor.ts";
import { MAIN_SECTION, type ReaderSlot, type ReaderSide } from "./source-reader-document.ts";
import { readerComparesSides, readerContentSide, readerShowsChangeMarks, sourceReaderChange, type SourceReaderChange } from "./source-reader-change.ts";
import type { SourceComparisonSession, ComparisonMode } from "../../../api/source-comparison-session.ts";
import { comparisonWindow, displayStart, sameWindow } from "./source-reader-window.ts";
import { editorDisplayPrefs } from "../editor/editor-display-prefs.ts";
import type { EditorDisplayPrefs } from "../editor/codemirror-theme.ts";

export const SOURCE_COMPARISON_FAILURE = {
  code: "source_comparison_unavailable",
  title: "Could not read the comparison",
  suggestedAction: "Reopen the file or comparison to read it again.",
};
export const SOURCE_SEARCH_FAILURE = {
  code: "source_search_unavailable",
  title: "Search in file failed",
  suggestedAction: "Search again, or reopen the file if it keeps failing.",
};

export type SourceReaderHandle = { gotoLine(): Promise<void>; revealLine(line: number, column?: number): Promise<void>; focus(): void; fullFile(): void; content(): Promise<string>; revealRow(row: number, match?: SourceReaderMatch): Promise<void> };

// Inline readers leave the DOM with their virtual row. Retain one bounded window
// per access so remounting does not collapse a tall comparison before its fetch.
const inlineWindows = new WeakMap<SourceReaderAccess, {
  mode: ComparisonMode; change: SourceReaderChange; slots: ReaderSlot[]; height?: number;
}>();

export function SourceReader(props: {
  /** Failures are reported to this project's notices. */
  projectId?: string;
  access: SourceReaderAccess;
  path: string;
  current?: boolean;
  wrap?: boolean;
  fontSize?: number;
  fontFamily?: string;
  lineHeight?: number;
  lineNumbers?: boolean;
  displayPrefs?: EditorDisplayPrefs;
  split?: boolean;
  /** Inline readers end at their last line; standalone readers let it reach the top. */
  scrollPastEnd?: boolean;
  onContributor?: (author: SourceContributor, line: number) => void;
  primaryFind?: boolean;
  find?: boolean;
  onReady?: (ready: boolean) => void;
  restoreViewState?: (sha: string) => DenReaderViewState | undefined;
  onViewState?: (sha: string, state: DenReaderViewState) => void;
  onHandle?: (handle: SourceReaderHandle | undefined) => void;
  leadingActions?: JSX.Element;
  trailingActions?: JSX.Element;
}) {
  const facts = createReaderFacts(() => props.path, (author, line) => props.onContributor?.(author, line));
  const live = useResidentLive();
  const interactive = useResidentInteractive();
  const [host, setHost] = createSignal<HTMLDivElement>();
  const [beforeHost, setBeforeHost] = createSignal<HTMLDivElement>();
  const [afterHost, setAfterHost] = createSignal<HTMLDivElement>();
  const [error, setError] = createSignal<unknown>();
  const initialMode = props.split && !props.current ? "split" : props.current ? "after" : "changes";
  const heldWindow = props.scrollPastEnd === false ? inlineWindows.get(props.access) : undefined;
  const restoredWindow = heldWindow?.mode === initialMode ? heldWindow : undefined;
  const [retainedHeight, setRetainedHeight] = createSignal<number | undefined>(restoredWindow?.height);
  let publishedWindow: NonNullable<typeof heldWindow> | undefined = restoredWindow;
  const [summarized, setSummarized] = createSignal<{ access: SourceReaderAccess; change: SourceReaderChange } | undefined>(
    restoredWindow ? { access: props.access, change: restoredWindow.change } : undefined,
  );
  // File-change classification waits for the host summary.
  const change = () => { const held = summarized(); return held?.access === props.access ? held.change : undefined; };
  const fullSide = (): ReaderSide => readerContentSide(change() ?? "changed");
  const [mode, setMode] = createSignal<ComparisonMode>(props.current ? "after" : "changes");
  const [contentHeight, setContentHeight] = createSignal(100);
  const split = createMemo(() => !!props.split && mode() === "changes" && change() === "changed");
  const prefs = createMemo<EditorDisplayPrefs>(() => {
    if (props.displayPrefs) return props.displayPrefs;
    const defaults = editorDisplayPrefs();
    return { ...defaults, wordWrap: props.wrap ?? defaults.wordWrap, fontSize: props.fontSize ?? defaults.fontSize,
      fontFamily: props.fontFamily ?? defaults.fontFamily, lineHeight: props.lineHeight ?? defaults.lineHeight,
      lineNumbers: props.lineNumbers ?? defaults.lineNumbers };
  });
  let slots: ReaderSlot[] = restoredWindow?.slots ?? [];
  let editors: ReaderEditor[] = [];
  let splitEditors: ReaderSplit | undefined;
  let preparation: Promise<void> = Promise.resolve();
  let generation = 0;
  let displayed: { access: SourceReaderAccess; mode: ComparisonMode } | undefined;
  let controller = new AbortController();
  let session: SourceComparisonSession | undefined;
  let detach: (() => void) | undefined;
  let stopUpdates: (() => void) | undefined;
  let followingProjection = false;
  let frames: SourceComparisonFrame[] = [];
  let rangeGeneration = 0;
  let navigating = false;
  let navigationGeneration = 0;
  let snapshotSHA = "";
  const captureViewState = (editor: ReaderEditor | undefined): void => {
    // View-state capture requires layout reads.
    if (props.onViewState && snapshotSHA && editor && editor.view.scrollDOM.clientHeight > 0 && editor.view.scrollDOM.getClientRects().length > 0) {
      props.onViewState?.(snapshotSHA, { selection: editor.selection(), viewport: editor.viewportAnchor() });
    }
  };
  let savedEditorState: { access: SourceReaderAccess; selection?: ReaderSelection; viewport?: DenReaderViewState["viewport"] } | undefined;
  const reportError = (cause: unknown): void => { setError(cause); };
  // A projection rebase cancels reads in the outgoing coordinates; the followed window rescans the viewport.
  const superseded = (cause: unknown): boolean => cause instanceof DOMException && cause.name === "AbortError";
  const publish = (next: ReaderSlot[], reset = false) => {
    // Loads rebuild windows from fresh objects; an unchanged window keeps its published slots.
    if (!reset && sameWindow(slots, next)) return;
    slots = next;
    for (const editor of editors) editor.setRows(next, reset);
  };

  const publishFrames = (next: SourceComparisonFrame[], reset = false) => {
    const anchor = reset ? undefined : splitEditors?.viewportAnchor() ?? editors[0]?.viewportAnchor();
    const window = comparisonWindow(next, session?.state()?.comparison?.summary?.rows ?? 0, prefs().fontSize * prefs().lineHeight);
    frames = next;
    publish(window, reset);
    const classification = untrack(change);
    if (props.scrollPastEnd === false && displayed && classification) {
      publishedWindow = { mode: displayed.mode, change: classification, slots, height: retainedHeight() };
      inlineWindows.set(displayed.access, publishedWindow);
    }
    if (anchor) {
      if (splitEditors) splitEditors.restoreViewport(anchor);
      else editors[0]?.restoreViewport(anchor);
    }
  };

  const followProjection = (): void => {
    const current = session, state = current?.state(), painted = frames[0];
    if (followingProjection || controller.signal.aborted || !untrack(live) || navigating || untrack(error) || !current || state?.state !== "ready"
      || !painted || painted.view_id !== state.id || painted.intent_revision !== state.intent_revision
      || painted.projection_revision === state.projection_revision) return;
    followingProjection = true;
    const token = generation, signal = controller.signal;
    void preparation.then(async () => {
      if (token !== generation || current !== session || signal.aborted || navigating) return;
      const frame = await current.frameAt({ row: painted.anchor.row }, signal);
      if (token !== generation || current !== session || signal.aborted) return;
      await loadWindow(frame.span.start);
    }).catch(cause => { if (token === generation && !signal.aborted && !superseded(cause)) reportError(cause); })
      .finally(() => { followingProjection = false; followProjection(); });
  };

  async function loadWindow(offset: number, reset = false): Promise<void> {
    const current = session, token = generation, range = ++rangeGeneration;
    if (!current) return;
    const center = Math.max(0, Math.floor(offset / 200) * 200);
    const count = current.state()?.extent.rows ?? 0;
    const offsets = [center, center - 200, center + 200].filter(value => value >= 0 && value < count);
    const next = await Promise.all(offsets.map(value => current.frame(value, 200, controller.signal)));
    if (token !== generation || current !== session || range !== rangeGeneration) return;
    publishFrames(next.map(page => frames.find(previous => previous.view_id === page.view_id && previous.projection_revision === page.projection_revision
      && previous.span.start === page.span.start && previous.span.end > page.span.end) ?? page), reset);
    props.onReady?.(true);
    followProjection();
  }

  async function loadRange(slot: ReaderSlot, offset = displayStart(slot)): Promise<void> {
    if (!live() || error() || navigating || !slots.includes(slot)) return;
    const current = session, token = generation;
    if (!current) return;
    try {
      if (slot.pending) await loadWindow(offset);
      else {
        await current.unfold(slot.index, slot.end);
        if (current !== session || token !== generation) return;
        const frame = await current.frameAt({ row: slot.index }, controller.signal);
        if (current !== session || token !== generation) return;
        publishFrames([frame]);
        await loadWindow(frame.span.start);
      }
    } catch (cause) {
      if (token === generation && !controller.signal.aborted && !superseded(cause)) { reportError(cause); props.onReady?.(true); }
    }
  }

  /** An inline reader holds its rows' own height, never the box its reserve props open. */
  const retainContentHeight = (): void => {
    if (props.scrollPastEnd !== false) return;
    const height = Math.max(0, ...editors.map(editor => editor.view.contentHeight));
    if (height <= 0) return;
    setRetainedHeight(height);
    if (publishedWindow) publishedWindow.height = height;
    const held = inlineWindows.get(props.access);
    if (held) held.height = height;
  };
  // Editor construction runs untracked to prevent extension reads from subscribing the effect.
  createEffect(() => {
    const after = afterHost(), before = beforeHost(), divided = split();
    if (!after || divided && !before) return;
    untrack(() => buildEditors(after, before, divided));
  });

  function buildEditors(after: HTMLElement, before: HTMLDivElement | undefined, divided: boolean): void {
    const make = (parent: HTMLElement, side?: ReaderSide) => new ReaderEditor({
      surface: "reader",
      parent, side, scrollPastEnd: props.scrollPastEnd, changes: () => readerShowsChangeMarks(untrack(change) ?? "changed") && ["changes", "full"].includes(untrack(mode)), facts: facts.show, hideFacts: facts.leave, prefs: untrack(prefs), access: () => props.access, fullSide,
      load: (slot, offset) => { void loadRange(slot, offset); }, error: reportError,
      heightChanged: (_editor, height) => { setContentHeight(Math.max(60, height + 36)); splitEditors?.measure(); retainContentHeight(); },
      scroll: editor => { splitEditors?.scroll(editor); captureViewState(editor); },
      viewportInput: editor => splitEditors?.input(editor),
      selectionChanged: captureViewState,
    });
    if (divided && before) {
      const left = make(before, "before"), right = make(after, "after");
      editors = [left, right];
      splitEditors = new ReaderSplit(left, right);
    } else editors = [make(after)];
    for (const editor of editors) editor.setRows(slots, true);
    const restored = savedEditorState;
    if (restored?.access === props.access) {
      const editor = editors.at(-1);
      if (restored.selection) editor?.restoreSelection(restored.selection);
      if (restored.viewport) editor?.restoreViewport(restored.viewport);
    }
    const editorAccess = props.access;
    onCleanup(() => {
      const editor = editors.find(editor => editor.view.hasFocus) ?? editors.at(-1);
      captureViewState(editor);
      savedEditorState = { access: editorAccess, selection: editor?.selection(), viewport: editor?.viewportAnchor() };
      splitEditors?.destroy(); splitEditors = undefined; for (const editor of editors) editor.destroy(); editors = [];
    });
  }
  createEffect(() => { const value = prefs(); for (const editor of editors) editor.prefs(value); });
  createEffect(() => {
    void findController.query(); void findController.isOpen();
    for (const editor of editors) editor.clearMatch();
  });
  createEffect(() => {
    const active = live();
    if (!active) { generation++; detach?.(); detach = undefined; controller.abort(); return; }
    const access = props.access;
    if (displayed && displayed.access !== access) setMode(props.current ? "after" : "changes");
    const requestedMode = props.split && mode() === "changes" ? "split" : mode();
    if (displayed?.access === access && session) {
      const current = session;
      const modeChanged = displayed.mode !== requestedMode;
      displayed.mode = requestedMode;
      if (modeChanged) setRetainedHeight(undefined);
      const viewport = savedEditorState?.access === access && savedEditorState.viewport
        || editors.at(-1)?.viewportAnchor();
      savedEditorState = undefined;
      generation++; controller.abort();
      controller = new AbortController();
      const token = generation;
      detach ??= current.attach();
      preparation = (async () => {
        if (!modeChanged) { await current.refresh(); await loadWindow(viewport?.rank ?? frames[0]?.span.start ?? 0); return; }
        const previous = await current.frame(viewport?.rank ?? frames[0]?.span.start ?? 0, 1, controller.signal);
        if (token !== generation) return;
        const classification = untrack(change);
        const resolvedMode = readerComparesSides(classification ?? "changed") ? requestedMode : readerContentSide(classification ?? "changed");
        await current.mode(resolvedMode);
        if (token !== generation) return;
        const frame = await current.frameAt({ row: previous.rows[0]?.index ?? 0 }, controller.signal);
        if (token !== generation) return;
        await loadWindow(frame.span.start);
        if (token !== generation) return;
        for (const editor of editors) editor.restoreViewport({ rank: frame.span.start, fraction: viewport?.fraction ?? 0 });
      })().catch(cause => { if (token === generation && !controller.signal.aborted) reportError(cause); });
      return;
    }
    const previous = session;
    stopUpdates?.(); stopUpdates = undefined;
    session = undefined; detach?.(); detach = undefined;
    if (previous) void previous.close().catch(reportError);
    displayed = { access, mode: requestedMode };
    generation++; controller.abort(); controller = new AbortController();
    const token = generation;
    snapshotSHA = "";
    // The current comparison stays visible until the next window loads.
    setError(undefined); props.onReady?.(false);
    preparation = access.summary().then(async reader => {
      if (token !== generation) return;
      const classification = sourceReaderChange(reader);
      setSummarized({ access, change: classification });
      const resolvedMode = readerComparesSides(classification) ? requestedMode : readerContentSide(classification);
      const next = await access.presentation({ mode: resolvedMode }, controller.signal);
      if (token !== generation) { await next.close(); return; }
      session = next;
      stopUpdates = next.subscribe(followProjection);
      detach = next.attach();
      await next.ready(controller.signal);
      if (token !== generation) return;
      if (!reader.rows) { inlineWindows.delete(access); setRetainedHeight(undefined); publish([], true); props.onReady?.(true); return; }
      const restored = props.restoreViewState?.(reader.after.sha256 ?? "");
      const offset = Math.min(Math.max(0, reader.rows - 1), restored?.viewport?.rank ?? 0);
      // Nothing is painted to rescan yet, so a read cancelled by a coordinate change loads again in the current ones.
      // A released session cancels every read; it stays a failure.
      for (;;) {
        try { await loadWindow(offset, true); break; }
        catch (cause) { if (token !== generation || controller.signal.aborted || next.isReleased() || !superseded(cause)) throw cause; }
      }
      if (token !== generation) return;
      for (const editor of editors) {
        if (restored?.selection) editor.restoreSelection(restored.selection);
        if (restored?.viewport) editor.restoreViewport(restored.viewport);
      }
      snapshotSHA = reader.after.sha256 ?? "";
    }).catch(cause => {
      // Failures clear retained rows to avoid showing the wrong comparison.
      if (token === generation && !controller.signal.aborted) { inlineWindows.delete(access); setRetainedHeight(undefined); publish([], true); reportError(cause); props.onReady?.(true); }
    });
  });

  async function revealRow(row: number, match?: SourceReaderMatch): Promise<void> {
    const token = generation, navigation = ++navigationGeneration;
    navigating = true;
    rangeGeneration++;
    try {
      await preparation;
      if (token !== generation || navigation !== navigationGeneration) return;
      if (editors.some(editor => editor.reveal(MAIN_SECTION, row, match))) return;
      const current = session;
      if (!current) return;
      const frame = await current.reveal(row, controller.signal);
      if (token !== generation || current !== session || navigation !== navigationGeneration) return;
      publishFrames([frame]);
      await loadWindow(frame.span.start);
      if (token !== generation || current !== session || navigation !== navigationGeneration) return;
      for (const editor of editors) editor.reveal(MAIN_SECTION, row, match);
    } catch (cause) { if (token === generation) throw cause; }
    finally { if (navigation === navigationGeneration) { navigating = false; followProjection(); } }
  }

  const handle: SourceReaderHandle = {
    async gotoLine() {
      const token = generation;
      try {
        const summary = await props.access.summary();
        if (token !== generation) return;
        const editor = editors.find(editor => editor.view.hasFocus) ?? editors[editors.length - 1];
        const row = editor?.document.at(editor.view.state.selection.main.head)?.row;
        openGotoLine({ lineCount: Math.max(1, summary[fullSide()].lines), currentLine: row?.after_line || row?.before_line || 1, focus: handle.focus,
          goTo: (line, column) => { void handle.revealLine(line, column).catch(reportError); },
        });
      } catch (cause) { if (token === generation) reportError(cause); }
    },
    async revealLine(line, column = 1) {
      const token = generation;
      try {
        await preparation;
        if (token !== generation) return;
        const current = session;
        if (!current) return;
        const located = await current.locate({ line, side: fullSide() }, controller.signal);
        if (token !== generation) return;
        let frame = await current.reveal(located.anchor.row, controller.signal);
        let selected = frame.rows.find(row => row.index === located.anchor.row);
        // Long source lines span multiple bounded fragments.
        for (;;) {
          if (token !== generation) return;
          let done = false;
          for (const slot of frame.rows) {
            const row = fullSide() === "after" ? slot.peer ?? slot : slot;
            const originalLine = fullSide() === "after" ? row.after_line : row.before_line;
            if (originalLine !== line) { if (selected) done = true; continue; }
            selected = row;
            if ((row.column ?? 0) + row.text.replace(/\n$/, "").length >= column - 1) { done = true; break; }
          }
          if (done || frame.span.end >= frame.extent.rows) break;
          frame = await current.frame(frame.span.end, 200, controller.signal);
        }
        if (selected) {
          const at = Math.max(0, Math.min(selected.text.replace(/\n$/, "").length, column - 1 - (selected.column ?? 0)));
          await revealRow(selected.index, { row: selected.index, from: at, to: at });
        }
      } catch (cause) { if (token === generation) throw cause; }
    },
    focus: () => (editors.find(editor => editor.view.hasFocus) ?? editors[editors.length - 1])?.view.focus(),
    fullFile: () => setMode("full"), content: () => props.access.content(fullSide()), revealRow,
  };
  createEffect(() => { props.onHandle?.(handle); onCleanup(() => props.onHandle?.(undefined)); });
  onCleanup(() => { generation++; controller.abort(); stopUpdates?.(); detach?.(); if (session) void session.close().catch(() => {}); props.onReady?.(false); });
  bindFindableView({ id: `source-reader:${crypto.randomUUID()}`, root: host, primary: () => !!props.primaryFind && interactive(), enabled: interactive });
  observeSurfaceFailure(SOURCE_COMPARISON_FAILURE, error, () => props.projectId);
  const searchState = bindReaderFind({ access: () => props.find === false || !interactive() ? undefined : props.access, host, reveal: revealRow });

  observeSurfaceFailure(SOURCE_SEARCH_FAILURE, searchState.error, () => props.projectId);

  return <div ref={setHost} class="den-source-reader" data-testid="source-reader" data-split={split() ? "true" : undefined} style={{ "--source-reader-content-height": `${contentHeight()}px`, "--source-reader-line-height": `${prefs().fontSize * prefs().lineHeight}px` }}>
    <div class={props.current ? "den-source-reader__actions" : "den-document-actions"}>
      {props.leadingActions}
      <Show when={!props.current && change() === "changed"}>
        <Show when={mode() === "changes"} fallback={<button type="button" class="den-document-action" onClick={() => setMode("changes")}>Changes</button>}>
          <button type="button" class="den-document-action" onClick={() => setMode("full")}>Full file</button>
        </Show>
      </Show>
      {props.trailingActions}
    </div>
    <Show when={searchState.pending()}><span role="status">Finding in file…</span></Show>
    {facts.card}
    <Show when={change() === "absent"}><div class="den-source-reader__state" role="status">The file doesn’t exist on either side of this comparison.</div></Show>
    <div class="den-source-reader__editors" aria-label={`Contents of ${props.path}`} hidden={change() === "absent"}
      style={{ "min-height": props.scrollPastEnd === false && retainedHeight() ? `${retainedHeight()}px` : undefined }}>
      <Show when={split()}><div class="den-source-reader__editor" ref={setBeforeHost} /></Show>
      <div class="den-source-reader__editor" ref={setAfterHost} />
    </div>
  </div>;
}
