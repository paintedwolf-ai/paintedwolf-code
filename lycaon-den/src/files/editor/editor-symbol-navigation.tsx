import { For, Show, createEffect, createMemo, createSignal, createUniqueId, onCleanup, untrack } from "solid-js";
import type { SourceSymbol } from "../../api/types.ts";
import { revealElementInScrollport } from "../../platform/scrolling/scrollport-motion.ts";
import { setOverviewSymbolMarks } from "../../components/source/annotations/overview-ruler.ts";
import { filterSymbolsByQuery } from "../tree/file-inventory.ts";
import { fetchCachedSourceSymbols } from "../source/source-symbols-cache.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

type SymbolNavigationDependencies = Pick<FilesEditorScope, "projectId" | "sessionId" | "client" | "buffer" | "live" |
  "chromeReady" | "currentView"> & {
  onSymbolJump: (line: number) => void;
};

export function createEditorSymbolNavigation({ projectId, sessionId, client, buffer, live, chromeReady,
  currentView: editorView, onSymbolJump }: SymbolNavigationDependencies) {
  let symbolTriggerEl: HTMLElement | undefined;
  let filterEl: HTMLInputElement | undefined;
  let menuEl: HTMLDivElement | undefined;
  const [symbolMenuOpen, setSymbolMenuOpen] = createSignal(false);
  const [symbolFilter, setSymbolFilter] = createSignal("");
  const [symbolActive, setSymbolActive] = createSignal(0);
  const symbolListId = createUniqueId();
  const [symbols, setSymbols] = createSignal<readonly SourceSymbol[]>([]);
  const [symbolsStatus, setSymbolsStatus] = createSignal<"loading" | "ready" | "unavailable">("loading");
  const filteredSymbols = createMemo(() =>
    filterSymbolsByQuery(symbols(), symbolFilter()),
  );

  const openSymbolMenu = (trigger: HTMLElement) => {
    symbolTriggerEl = trigger;
    setSymbolFilter("");
    setSymbolActive(0);
    setSymbolMenuOpen(true);
    queueMicrotask(() => filterEl?.focus());
  };

  /** Keyboard dismissal restores editor focus. */
  const closeSymbolMenu = (focus: "editor" | "keep") => {
    if (!untrack(symbolMenuOpen)) return;
    setSymbolMenuOpen(false);
    setSymbolFilter("");
    if (focus === "editor") editorView()?.focus();
  };

  const toggleSymbolMenu = (trigger: HTMLElement) => {
    if (untrack(symbolMenuOpen) && symbolTriggerEl === trigger) {
      closeSymbolMenu("keep");
      return;
    }
    openSymbolMenu(trigger);
  };

  const selectSymbol = (line: number) => {
    closeSymbolMenu("keep");
    onSymbolJump(line);
  };

  const moveSymbolActive = (next: number) => {
    const rows = filteredSymbols();
    if (rows.length === 0) return;
    const idx = (next + rows.length) % rows.length;
    setSymbolActive(idx);
    const row = menuEl?.querySelector(`[data-symbol-index="${idx}"]`);
    if (row) revealElementInScrollport(row);
  };

  const markSymbols = (rows: Parameters<typeof setOverviewSymbolMarks>[1]) => {
    const view = editorView();
    if (view) setOverviewSymbolMarks(view, rows);
  };

  const observeSource = () => {
  // Outline results apply only to the displayed revision.
  createEffect(() => {
    if (!live()) return;
    const c = client();
    if (!chromeReady() || !c || buffer().jobId) {
      setSymbols([]);
      setSymbolsStatus(!chromeReady() ? "loading" : "unavailable");
      markSymbols([]);
      return;
    }
    const sha = buffer().baseSha256;
    setSymbolsStatus("loading");
    const controller = new AbortController();
    void fetchCachedSourceSymbols({
      client: c,
      projectId: projectId(),
      sessionId: sessionId(),
      rootId: buffer().rootId,
      path: buffer().path,
      sha256: sha,
      signal: controller.signal,
    }).then((rows) => {
      if (controller.signal.aborted) return;
      setSymbols(rows);
      setSymbolsStatus("ready");
      markSymbols(rows);
    }).catch(() => {
      if (controller.signal.aborted) return;
      setSymbols([]);
      setSymbolsStatus("unavailable");
      markSymbols([]);
    });
    onCleanup(() => {
      controller.abort();
    });
  });

  };
  return { symbolMenuOpen, symbolFilter, setSymbolFilter, symbolActive, setSymbolActive, symbolListId,
    symbols, symbolsStatus, filteredSymbols, closeSymbolMenu, toggleSymbolMenu, selectSymbol, moveSymbolActive,
    symbolTrigger: () => symbolTriggerEl, observeSource,
    setFilterElement: (element: HTMLInputElement) => { filterEl = element; },
    setMenuElement: (element: HTMLDivElement) => { menuEl = element; } };
}

/** The outline menu the crumb's symbol trigger opens: a filter over the file's symbols. */
export function FilesSymbolMenu(props: { symbols: ReturnType<typeof createEditorSymbolNavigation> }) {
  return (
    <Show when={props.symbols.symbolMenuOpen()}>
      <AnchoredSurface
        ref={(el) => {
          props.symbols.setMenuElement(el);
        }}
        class="den-files-editor__symbol-menu den-files-editor__symbol-menu--float"
        anchor={props.symbols.symbolTrigger}
        preferredSide="bottom"
        align="start"
        overflow="hidden"
        dismissOnScroll
        onDismiss={() => props.symbols.closeSymbolMenu("keep")}
        testId="files-crumb-symbol-menu"
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            props.symbols.closeSymbolMenu("editor");
          } else if (e.key === "ArrowDown") {
            e.preventDefault();
            props.symbols.moveSymbolActive(props.symbols.symbolActive() + 1);
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            props.symbols.moveSymbolActive(props.symbols.symbolActive() - 1);
          } else if (e.key === "Home") {
            e.preventDefault();
            props.symbols.moveSymbolActive(0);
          } else if (e.key === "End") {
            e.preventDefault();
            props.symbols.moveSymbolActive(props.symbols.filteredSymbols().length - 1);
          } else if (e.key === "Enter") {
            const sym = props.symbols.filteredSymbols()[props.symbols.symbolActive()];
            if (sym) {
              e.preventDefault();
              props.symbols.selectSymbol(sym.line);
            }
          }
        }}
      >
        <input
          ref={props.symbols.setFilterElement}
          type="search"
          class="den-files-editor__symbol-filter"
          data-testid="files-crumb-symbol-filter"
          placeholder="Filter symbols…"
          aria-label="Filter symbols"
          role="combobox"
          aria-expanded="true"
          aria-controls={props.symbols.symbolListId}
          aria-activedescendant={
            props.symbols.filteredSymbols().length > 0
              ? `${props.symbols.symbolListId}-${props.symbols.symbolActive()}`
              : undefined
          }
          value={props.symbols.symbolFilter()}
          onInput={(e) => {
            props.symbols.setSymbolFilter(e.currentTarget.value);
            props.symbols.setSymbolActive(0);
          }}
        />
        <Show
          when={props.symbols.filteredSymbols().length > 0}
          fallback={
            <p class="den-files-editor__symbol-empty">
              {props.symbols.symbolsStatus() === "loading" ? "Loading outline…"
                : props.symbols.symbolsStatus() === "unavailable" ? "The outline is unavailable. You can still read and edit this file."
                : props.symbols.symbols().length === 0
                ? "No symbols in this file"
                : "No symbols match that filter"}
            </p>
          }
        >
          {/* The list scrolls while the filter stays pinned. */}
          <Scrollport
            class="den-files-editor__symbol-scroll"
            contentAs="ul"
            contentClass="den-files-editor__symbol-list"
            content={{ id: props.symbols.symbolListId, role: "listbox", "aria-label": "Symbols in this file" }}
          >
              <For each={props.symbols.filteredSymbols()}>
                {(sym, i) => (
                  <li>
                    <button
                      type="button"
                      id={`${props.symbols.symbolListId}-${i()}`}
                      class="den-files-editor__symbol-row"
                      classList={{
                        "den-files-editor__symbol-row--active":
                          i() === props.symbols.symbolActive(),
                      }}
                      role="option"
                      aria-selected={i() === props.symbols.symbolActive()}
                      data-symbol-index={i()}
                      onClick={() => props.symbols.selectSymbol(sym.line)}
                    >
                      <span class="den-files-editor__symbol-kind">
                        {formatSentenceCase(sym.kind)}
                      </span>
                      <span>{sym.name}</span>
                      <span class="den-files-editor__symbol-line">
                        :{sym.line}
                      </span>
                    </button>
                  </li>
                )}
              </For>
          </Scrollport>
        </Show>
      </AnchoredSurface>
    </Show>
  );
}
