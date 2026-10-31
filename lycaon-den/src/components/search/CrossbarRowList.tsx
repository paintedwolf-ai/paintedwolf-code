import { For, Show, createEffect, createMemo, createSignal, onCleanup, type JSX } from "solid-js";
import type {
  ContributionSearchResult,
  ContributionSearchSource,
  ProjectRoot,
  SourceSearchOutside,
} from "../../api/types.ts";
import { basenameOfPath } from "../../api/project-path.ts";
import { localPathDestinationLabel } from "../../platform/navigation/open-local-path.ts";
import { ATTENTION_CLASS_LABEL } from "../../attention/attention-model.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
import {
  CROSSBAR_GOTO_KIND_LABEL,
  crossbarArm,
  crossbarAwaitsSymbolName,
  crossbarHitStatus,
  crossbarNoMatchLabel,
  type CrossbarLineTarget,
  type CrossbarRow,
} from "../../search/crossbar-model.ts";
import { CROSSBAR_MODE_LABEL, type CrossbarMode } from "../../search/crossbar-modes.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import type { CrossbarCommandState } from "../../contributions/dispatch.ts";
import {
  EscalateArrowIcon,
  GotoTargetIcon,
  PaletteSlotIcon,
  RecentQueryIcon,
  SearchKindIcon,
} from "./SearchIcons.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  CrossbarActionRow,
  actionStatusLabel,
  ContributionSearchLane,
  renderMatchUnderline,
} from "./CrossbarResults.tsx";

export type CrossbarRowListProps = {
  query: string;
  mode: CrossbarMode;
  activeMode: CrossbarMode;
  rows: readonly CrossbarRow[];
  selectable: readonly Exclude<CrossbarRow, { kind: "section" }>[];
  activeIndex: number;
  searchBusy: boolean;
  hitCount: number;
  coverageNote: string;
  lineTarget: CrossbarLineTarget | null;
  projectRoots?: readonly Pick<ProjectRoot, "id" | "label">[];
  contributionActions?: CrossbarCommandState;
  activeSource: ContributionSearchSource | null;
  activeSourceQuery: string;
  sourceResults: ContributionSearchResult[];
  sourceError: string;
  dialogEl: () => HTMLDivElement | undefined;
  onSelectIndex: (index: number) => void;
  onActivateRow: (row: Exclude<CrossbarRow, { kind: "section" }>) => void;
  onEscalateTo: (mode: CrossbarMode) => void;
  onActivateSourceResult: (source: ContributionSearchSource, result: ContributionSearchResult) => void;
  onConfigureSource: () => void;
  renderStatusBanner: (args: {
    actionStatus: string | null | undefined;
    showHitStatus: boolean;
    coverageNote: string;
    hitStatusLabel: string;
  }) => JSX.Element;
};

export function CrossbarRowList(props: CrossbarRowListProps) {
  // Escalate renders pinned below the list; everything else scrolls.
  const listRows = createMemo(() =>
    props.rows.filter(
      (r): r is Exclude<CrossbarRow, { kind: "escalate" }> => r.kind !== "escalate",
    ),
  );

  const escalateRow = createMemo(() =>
    props.rows.find((r): r is Extract<CrossbarRow, { kind: "escalate" }> => r.kind === "escalate"),
  );

  const hitStatus = createMemo(() =>
    crossbarHitStatus({
      query: props.query,
      mode: props.activeMode,
      searching: props.searchBusy,
      hitCount: props.hitCount,
    }),
  );

  // Show status when escalation is the only result.
  const showHitStatusRow = createMemo(() => {
    const arm = crossbarArm(props.query).kind;
    if (arm === "file-symbols") return !props.rows.some((r) => r.kind === "symbol");
    if (crossbarAwaitsSymbolName(props.query, props.mode)) return true;
    if (arm === "line") return !props.rows.some((r) => r.kind === "line");
    const status = hitStatus();
    if (status !== "searching" && status !== "empty") return false;
    return !props.rows.some(
      (r) =>
        r.kind === "action" ||
        r.kind === "hit" ||
        r.kind === "goto" ||
        r.kind === "inventory-file" ||
        r.kind === "symbol" ||
        r.kind === "revision" ||
        r.kind === "outside",
    );
  });

  const hitStatusLabel = createMemo(() => {
    const arm = crossbarArm(props.query);
    if (arm.kind === "file-symbols") {
      return props.lineTarget ? "No symbols in this file" : "Open a file to list its symbols";
    }
    if (crossbarAwaitsSymbolName(props.query, props.mode)) return "Type a symbol name";
    if (arm.kind === "line") {
      return props.lineTarget ? "Type a line number" : "Open a file to go to a line";
    }
    const status = hitStatus();
    return (
      props.coverageNote ||
      (status === "searching" ? "Searching…" : crossbarNoMatchLabel(props.activeMode))
    );
  });

  // End the visible list on a row boundary.
  let listFrame: HTMLDivElement | undefined;
  let listEl: HTMLDivElement | undefined;
  let listContent: HTMLElement | undefined;
  const [listEdges, setListEdges] = createSignal({ top: false, bottom: false });
  const syncListEdges = () => {
    const el = listEl;
    if (!el) return;
    const overflow = el.scrollHeight > el.clientHeight + 1;
    const top = overflow && el.scrollTop > 1;
    const bottom = overflow && el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setListEdges((prev) =>
      prev.top === top && prev.bottom === bottom ? prev : { top, bottom },
    );
  };
  const fitListRows = () => {
    const frame = listFrame;
    const el = listEl;
    const content = listContent;
    if (!frame || !el || !content) return;
    frame.style.maxHeight = "";
    const available = el.clientHeight;
    if (el.scrollHeight > available + 1) {
      const fit = wholeRowHeight(content, available);
      if (fit != null) frame.style.maxHeight = `${fit}px`;
    }
    syncListEdges();
  };
  const mountList = (frame: HTMLDivElement) => {
    listFrame = frame;
    if (typeof ResizeObserver === "undefined") return;
    // Refit when the dialog resizes; the frame is the wrap's only in-flow child.
    const observer = new ResizeObserver(fitListRows);
    observer.observe(frame);
    onCleanup(() => {
      observer.disconnect();
      if (listFrame === frame) {
        listFrame = undefined;
        listEl = undefined;
        listContent = undefined;
      }
    });
  };
  createEffect(() => {
    listRows();
    showHitStatusRow();
    // Rows land before their geometry does.
    const frame = requestAnimationFrame(fitListRows);
    onCleanup(() => cancelAnimationFrame(frame));
  });

  createEffect(() => {
    const idx = props.activeIndex;
    if (idx < 0) return;
    queueMicrotask(() => {
      const activeEl = (listEl ?? props.dialogEl())?.querySelector<HTMLElement>(
        ".den-crossbar__row--active",
      );
      activeEl?.scrollIntoView({ block: "nearest" });
    });
  });

  return (
    <Show
      when={!props.activeSource}
      fallback={
        <Show when={props.activeSource} keyed>
          {(source) => (
            <ContributionSearchLane
              source={source}
              query={props.activeSourceQuery}
              results={props.sourceResults}
              error={props.sourceError}
              searching={props.searchBusy}
              activeIndex={props.activeIndex}
              onHover={props.onSelectIndex}
              onActivate={(result) => props.onActivateSourceResult(source, result)}
              onConfigure={props.onConfigureSource}
            />
          )}
        </Show>
      }
    >
      <div
        class="den-crossbar__list-wrap"
        data-fade-top={listEdges().top ? "" : undefined}
        data-fade-bottom={listEdges().bottom ? "" : undefined}
      >
        <Scrollport
          ref={mountList}
          viewportRef={(el) => (listEl = el)}
          contentRef={(el) => (listContent = el)}
          class="den-crossbar__list"
          contentAs="ul"
          contentClass="den-crossbar__list-content"
          data-testid="crossbar-list"
          viewport={{ onScroll: syncListEdges }}
        >
          {props.renderStatusBanner({
            actionStatus: actionStatusLabel(props.contributionActions, props.query, props.mode),
            showHitStatus: showHitStatusRow(),
            coverageNote: props.coverageNote,
            hitStatusLabel: hitStatusLabel(),
          })}
          <For each={listRows()}>
            {(row) => {
              if (row.kind === "section") {
                return (
                  <li
                    class="den-crossbar__section"
                    data-testid={`crossbar-section-${row.id}`}
                  >
                    <span class="den-crossbar__section-label">{row.label}</span>
                    <Show when={row.more} keyed>
                      {(more) => (
                        <button
                          type="button"
                          class="den-crossbar__section-more"
                          data-testid={`crossbar-see-all-${more}`}
                          aria-label={`See all ${CROSSBAR_MODE_LABEL[more].toLowerCase()} results`}
                          tabIndex={-1}
                          onClick={() => props.onEscalateTo(more)}
                        >
                          See all
                        </button>
                      )}
                    </Show>
                  </li>
                );
              }
              const selIdx = () => props.selectable.findIndex((r) => r.id === row.id);
              const active = () => selIdx() === props.activeIndex;
              if (row.kind === "action") {
                return (
                  <CrossbarActionRow
                    command={row.command}
                    active={active()}
                    onHover={() => props.onSelectIndex(selIdx())}
                    onActivate={() => props.onActivateRow(row)}
                  />
                );
              }
              if (row.kind === "recent") {
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-recent"
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <RecentQueryIcon />
                      </span>
                      <span class="den-crossbar__primary">{row.query}</span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "goto") {
                const attention = () =>
                  row.target.kind === "session" ? row.target.attention : undefined;
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-goto"
                      data-goto-kind={row.target.kind}
                      data-attention={attention()?.class}
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <GotoTargetIcon kind={row.target.kind} />
                      </span>
                      <span class="den-crossbar__primary">{row.target.label}</span>
                      <Show when={row.target.context}>
                        {(ctx) => <span class="den-crossbar__secondary">{ctx()}</span>}
                      </Show>
                      {/* Attention status replaces the generic target kind. */}
                      <Show
                        when={attention()}
                        fallback={
                          <span class="den-crossbar__chip">
                            {CROSSBAR_GOTO_KIND_LABEL[row.target.kind]}
                          </span>
                        }
                      >
                        {(att) => (
                          <span
                            class="den-crossbar__chip den-attention-chip"
                            data-attention-class={att().class}
                          >
                            <span class="den-attention-chip__dot" aria-hidden="true" />
                            {ATTENTION_CLASS_LABEL[att().class]}
                          </span>
                        )}
                      </Show>
                    </button>
                  </li>
                );
              }
              if (row.kind === "inventory-file") {
                const file = () => row.file;
                const dirPrefix = () => {
                  const roots = props.projectRoots ?? [];
                  if (roots.length < 2) return "";
                  const label =
                    roots.find((root) => root.id === file().rootId)?.label ||
                    file().rootId;
                  return file().dir ? `@${label}/` : `@${label}`;
                };
                const dir = () => dirPrefix() + file().dir || ".";
                const dirIndexes = () => {
                  const offset = dirPrefix().length;
                  return file()
                    .matchIndexes.filter((index) => index < file().dir.length)
                    .map((index) => index + offset);
                };
                const baseIndexes = () => {
                  const offset = file().path.length - file().basename.length;
                  return file()
                    .matchIndexes.filter((index) => index >= offset)
                    .map((index) => index - offset);
                };
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-inventory-file"
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <SearchKindIcon kind="file" />
                      </span>
                      <span class="den-crossbar__primary">
                        {renderMatchUnderline(file().basename, baseIndexes())}
                      </span>
                      <span class="den-crossbar__secondary">
                        {renderMatchUnderline(dir(), dirIndexes())}
                      </span>
                      <span class="den-crossbar__chip">File</span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "symbol") {
                const sym = () => row.symbol;
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-symbol"
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <SearchKindIcon kind="code" />
                      </span>
                      <span class="den-crossbar__primary">
                        {renderMatchUnderline(sym().name, sym().matchIndexes ?? [])}
                      </span>
                      <span class="den-crossbar__secondary">:{sym().line}</span>
                      <span class="den-crossbar__chip">{sym().kind}</span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "line") {
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-line"
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <PaletteSlotIcon slot="goto-line" />
                      </span>
                      <span class="den-crossbar__primary">
                        {row.column != null
                          ? `Go to line ${row.line}, column ${row.column}`
                          : `Go to line ${row.line}`}
                      </span>
                      <span class="den-crossbar__secondary">{row.target.path}</span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "revision") {
                const root = () =>
                  (props.projectRoots ?? []).length > 1
                    ? props.projectRoots?.find((r) => r.id === row.comparison.root_id)?.label
                    : undefined;
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-revision"
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <PaletteSlotIcon slot="diff" />
                      </span>
                      <span class="den-crossbar__primary">{row.comparison.label}</span>
                      <Show when={root()}>
                        {(label) => (
                          <span class="den-crossbar__secondary">@{label()}</span>
                        )}
                      </Show>
                      <span class="den-crossbar__chip">
                        {row.comparison.kind === "commit"
                          ? "Commit"
                          : row.comparison.kind === "branch"
                            ? "Branch"
                            : "Range"}
                      </span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "outside") {
                const label = () =>
                  row.action === "reveal"
                    ? `Reveal in ${localPathDestinationLabel("file-manager")}`
                    : row.outside.kind === "directory"
                      ? `Open ${basenameOfPath(row.outside.path)} as a project`
                      : `Open ${basenameOfPath(outsideProjectFolder(row.outside))} as a project`;
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid={`crossbar-outside-${row.action}`}
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <PaletteSlotIcon
                          slot={row.action === "reveal" ? "folder-open" : "open-folder"}
                        />
                      </span>
                      <span class="den-crossbar__primary">{label()}</span>
                      <span class="den-crossbar__secondary">{row.outside.path}</span>
                    </button>
                  </li>
                );
              }
              if (row.kind === "hit") {
                const display = () => searchHitDisplay(row.hit);
                const secondary = () =>
                  display().context ?? row.hit.project_name?.trim();
                return (
                  <li>
                    <button
                      type="button"
                      class="den-crossbar__row"
                      classList={{ "den-crossbar__row--active": active() }}
                      data-testid="crossbar-hit"
                      data-hit-kind={row.hit.hit_kind}
                      onMouseEnter={() => props.onSelectIndex(selIdx())}
                      onClick={() => props.onActivateRow(row)}
                    >
                      <span class="den-crossbar__glyph" aria-hidden="true">
                        <SearchKindIcon kind={row.hit.hit_kind} />
                      </span>
                      <span class="den-crossbar__primary">
                        {renderMatchUnderline(
                          display().title,
                          display().titleMatches ?? [],
                        )}
                      </span>
                      <Show when={secondary()}>
                        {(ctx) => <span class="den-crossbar__secondary">{ctx()}</span>}
                      </Show>
                      <span class="den-crossbar__chip">{display().kindLabel}</span>
                    </button>
                  </li>
                );
              }
              return null;
            }}
          </For>
          <Show
            when={
              props.rows.length === 0 &&
              !showHitStatusRow() &&
              !actionStatusLabel(props.contributionActions, props.query, props.mode)
            }
          >
            <li class="den-crossbar__empty" data-testid="crossbar-empty">
              {props.query.trim()
                ? "No matching actions or results."
                : "Type to search or run an action."}
            </li>
          </Show>
        </Scrollport>
        <span
          class="den-crossbar__list-edge den-crossbar__list-edge--top"
          aria-hidden="true"
        />
        <span
          class="den-crossbar__list-edge den-crossbar__list-edge--bottom"
          aria-hidden="true"
        />
      </div>

      {/* Keep escalation stationary while hits stream in. */}
      <Show when={escalateRow()}>
        {(row) => {
          const selIdx = () => props.selectable.findIndex((r) => r.id === row().id);
          const active = () => selIdx() === props.activeIndex;
          return (
            <div class="den-crossbar__escalate-slot">
              <button
                type="button"
                class="den-crossbar__row den-crossbar__row--escalate"
                classList={{ "den-crossbar__row--active": active() }}
                data-testid="crossbar-escalate"
                onMouseEnter={() => props.onSelectIndex(selIdx())}
                onClick={() => props.onActivateRow(row())}
              >
                <span class="den-crossbar__glyph" aria-hidden="true">
                  <EscalateArrowIcon />
                </span>
                <span class="den-crossbar__primary">Show all results</span>
                <span class="den-crossbar__chord">
                  {bindingForHandler("search.escalate")}
                </span>
              </button>
            </div>
          );
        }}
      </Show>
    </Show>
  );
}

/** Fits complete rows without leaving a section header last. */
function wholeRowHeight(list: HTMLElement, available: number): number | null {
  const listTop = list.getBoundingClientRect().top - list.scrollTop;
  const paddingBottom = parseFloat(getComputedStyle(list).paddingBottom) || 0;
  let fit: number | null = null;
  let previous: number | null = null;
  for (const child of list.children) {
    // Only rows bound the fit.
    if (!(child instanceof HTMLLIElement)) continue;
    const bottom = child.getBoundingClientRect().bottom - listTop + paddingBottom;
    if (bottom > available) break;
    const isHeader = child.classList.contains("den-crossbar__section");
    fit = isHeader ? previous : bottom;
    previous = bottom;
  }
  return fit;
}

/** A directory opens as itself; a file opens its containing folder. */
export function outsideProjectFolder(outside: SourceSearchOutside): string {
  if (outside.kind === "directory") return outside.path;
  const cut = Math.max(outside.path.lastIndexOf("/"), outside.path.lastIndexOf("\\"));
  return cut > 0 ? outside.path.slice(0, cut) : outside.path;
}
