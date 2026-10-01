import { ThemeIcon } from "../../primitives/ThemeIcon.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { For, Show, createMemo, createEffect, createSignal, on, onCleanup } from "solid-js";
import { useRowAccordion } from "../../../chat/transcript/presentation/row-accordion.ts";
import type { FileEditLineStat } from "../../../chat/file-edit/file-edit-fold.ts";
import { openDiffViewer } from "../../../platform/navigation/in-app-diff.ts";
import { diffCollapsedPref, diffWordWrapPref } from "../../../settings/appearance/display-prefs.ts";
import { SourcePathLink } from "../SourcePathLink.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { SourceChangeChip } from "./SourceChangeChip.tsx";
import { LineStat, WholeFileDetails } from "./SourceDiffParts.tsx";
import { wholeFileChange } from "../reader/source-reader-change.ts";
import { animateHeightToggle, heightToggleTargetFor } from "../../../ui/height-toggle-motion.ts";
import { SOURCE_COMPARISON_FAILURE, SOURCE_SEARCH_FAILURE, SourceReader, type SourceReaderHandle } from "../reader/SourceReader.tsx";
import { observeSurfaceFailure } from "../../../notices/surface-failure.ts";
import { bindReaderFind } from "../reader/source-reader-find.ts";
import { useTranscriptViewport } from "../../../chat/stream/transcript-viewport.tsx";
import { sameDiffRowRevisions, type DiffRowSource } from "./diff-row-source.ts";
import type { TranscriptDisclosureKey } from "../../../chat/transcript/presentation/transcript-disclosure-key.ts";

type Props = {
  source: DiffRowSource;
  onNetStat?: (stat: FileEditLineStat) => void;
  chrome?: "card" | "row";
  /** Holds the measured net-diff height until the reader loads. */
  reserve?: number;
  /** Transcript identity of this disclosure. */
  disclosureKey?: TranscriptDisclosureKey;
};

type ActiveRevision = "net" | number;

function DisclosureCaret(props: { open: boolean }) {
  return (
    <span
      class={`den-tool-chicklet-caret den-file-edit-diff-caret${
        props.open ? " den-file-edit-diff-caret--open" : ""
      }`}
      aria-hidden="true"
    />
  );
}

/** One file's change, with its net range and the revisions that composed it. */
export function SourceDiffRow(props: Props) {
  const viewport = useTranscriptViewport();
  const accordion = useRowAccordion();
  // Virtualized rows retain disclosure state across remounts.
  const [openOverride, setOpenOverride] = createSignal<boolean | null>(
    accordion?.heldOpen?.(props.source.key) ?? null,
  );
  const [active, setActive] = createSignal<ActiveRevision>("net");
  const [findRoot, setFindRoot] = createSignal<HTMLElement | null>(null);
  let root: HTMLElement | undefined;
  let header: HTMLElement | undefined;

  const asRow = () => props.chrome === "row";
  // Rows start closed; cards follow the display preference.
  const bodyOpen = () =>
    openOverride() ?? (asRow() ? false : !diffCollapsedPref());

  // A source rebuilt from the same writes leaves the strip and its comparison alone.
  const revisions = createMemo(() => props.source.revisions, undefined, { equals: sameDiffRowRevisions });
  const hasRevisions = () => revisions().length > 1;
  const activeIndex = (): number | null => {
    const at = active();
    return at === "net" || !revisions()[at] ? null : at;
  };
  const [hostStat, setHostStat] = createSignal<FileEditLineStat | null>(null);
  const [statError, setStatError] = createSignal<unknown>();
  const [readerHandle, setReaderHandle] = createSignal<SourceReaderHandle>();
  const access = createMemo(() => props.source.access(activeIndex()));
  const stat = () => props.source.stat(activeIndex()) ?? hostStat();
  createEffect(() => {
    const reader = access(); const index = activeIndex();
    setHostStat(null); setStatError(undefined);
    // Measured comparisons already include line counts.
    if (!reader || (revisions().length <= 1 && !bodyOpen())) return;
    let cancelled = false;
    void reader.summary().then(summary => {
      if (cancelled) return;
      setHostStat(summary);
      if (index == null) props.onNetStat?.(summary);
    }).catch(cause => { if (!cancelled) setStatError(cause); });
    onCleanup(() => { cancelled = true; });
  });
  const searchState = bindReaderFind({ access, host: findRoot, collapsed: () => !bodyOpen(),
    expand: () => { const previous = openOverride(); setOpenOverride(true); return () => setOpenOverride(previous); },
    reveal: async (row, match) => { setOpenOverride(true); await Promise.resolve(); await readerHandle()?.revealRow(row, match); },
  });

  // A source without a project has no readable access, so nothing to report.
  observeSurfaceFailure(SOURCE_COMPARISON_FAILURE, statError, () => props.source.projectId);
  observeSurfaceFailure(SOURCE_SEARCH_FAILURE, searchState.error, () => props.source.projectId);

  const change = () => props.source.change(activeIndex());
  const path = () => props.source.path(activeIndex());
  const isNoop = () => props.source.isNoop(activeIndex());
  const wholeFile = () => wholeFileChange(change());

  function openFullscreen() {
    const reader = access(); if (!reader) return;
    setOpenOverride(true);
    openDiffViewer({ projectId: props.source.projectId, path: path(), reader, change: change() });
  }

  const inlineWrap = () => diffWordWrapPref();
  const [readerReady, setReaderReady] = createSignal(false);
  // Reserved height applies only to the net diff.
  const reserved = () =>
    props.reserve && !readerReady() && activeIndex() === null && !isNoop() ? `${props.reserve}px` : undefined;

  // Find reveals are instant; user toggles animate.
  function applyOpen(next: boolean): void {
    const currentTarget = (root ? heightToggleTargetFor(root) : undefined) ?? bodyOpen();
    if (next === currentTarget) return;
    const direction = next ? "open" : "close";
    const finishMotion = viewport?.beginDisclosureMotion(props.disclosureKey, direction);
    if (!root) {
      setOpenOverride(next);
      finishMotion?.();
      return;
    }
    animateHeightToggle(root, {
      direction,
      showBody: () => setOpenOverride(true),
      // Conditional unmounting needs no display hold.
      hideBody: () => {},
      closeBody: () => setOpenOverride(false),
      resetBodyVisibility: () => {},
      collapsedHeight: () => header?.getBoundingClientRect().height ?? 0,
      bodyStillOpen: () => bodyOpen(),
      onSettled: finishMotion,
    });
  }

  if (accordion) {
    onCleanup(
      accordion.register({
        key: props.source.key,
        el: () => root ?? null,
        domOpen: () => bodyOpen(),
        apply: applyOpen,
      }),
    );
    if (accordion.recordOpen) {
      const record = accordion.recordOpen;
      // Only this row's own state is recorded; the record's reads are not dependencies.
      createEffect(on(bodyOpen, (open) => record(props.source.key, open)));
    }
  }

  function toggleDiff() {
    if (accordion) {
      accordion.toggle(props.source.key);
      return;
    }
    const currentTarget = (root ? heightToggleTargetFor(root) : undefined) ?? bodyOpen();
    applyOpen(!currentTarget);
  }

  return (
    <article
      ref={(el) => {
        root = el;
        setFindRoot(el);
      }}
      class="den-file-edit-diff"
      classList={{ "den-transcript-disclosure-card": !asRow() }}
      data-testid="file-edit-diff"
      data-path={path()}
      data-chrome={asRow() ? "row" : "card"}
      data-expanded={bodyOpen() ? "true" : "false"}
      data-disclosure-key={props.disclosureKey}
    >
      <div
        ref={(el) => { header = el; }}
        class="den-file-edit-diff-header"
        onClick={toggleDiff}
      >
        {/* The path handles clicks separately. */}
        <SourcePathLink
          truncate
          strong
          path={path()}
          rootId={props.source.rootId(activeIndex())}
          openable={props.source.openable(activeIndex())}
          change={wholeFileChange(change())}
          projectId={props.source.projectId}
          jobId={props.source.jobId}
          rootRefs={props.source.rootRefs}
          chatDestination={props.source.chatDestination}
        />
        <span class="den-file-edit-diff-fill" aria-hidden="true" />
        <SourceChangeChip change={change()} />
        <Show
          when={!isNoop()}
          fallback={
            <span
              class="den-file-edit-diff-stat den-file-edit-diff-stat--noop"
              data-testid="file-edit-diff-stat"
            >
              no net change
            </span>
          }
        >
          <Show keyed when={stat()} fallback={<Show when={!statError()}><span role="status">Comparing…</span></Show>}>{value => <LineStat stat={value} />}</Show>
        </Show>
        <Show when={hasRevisions()}>
          <span
            class="den-file-edit-diff-writes"
            data-testid="file-edit-diff-writes"
          >
            {revisions().length} edits
          </span>
        </Show>
        <button
          type="button"
          class="den-file-edit-diff-disclosure"
          aria-expanded={bodyOpen()}
          aria-label={`${bodyOpen() ? "Collapse" : "Show"} diff for ${path()}`}
          onClick={(event) => {
            event.stopPropagation();
            toggleDiff();
          }}
        >
          <Show when={asRow()} fallback={<DisclosureCaret open={bodyOpen()} />}>
            <span class="den-row-mark" aria-hidden="true" />
          </Show>
        </button>
      </div>

      <Show when={searchState.pending()}><span role="status">Finding in file…</span></Show>
      <Show when={bodyOpen()}>
        <div class="den-file-edit-diff-body">
          <Show when={hasRevisions()}>
            <Scrollport
              class="den-file-edit-diff-strip"
              contentClass="den-file-edit-diff-strip__content"
              axis="x"
              role="group"
              aria-label={props.source.revisionsLabel}
              data-testid="file-edit-diff-strip"
            >
              <button
                type="button"
                class="den-file-edit-diff-step"
                aria-pressed={activeIndex() === null}
                onClick={() => setActive("net")}
              >
                {props.source.netLabel}
              </button>
              <span class="den-file-edit-diff-step-sep" aria-hidden="true" />
              <For each={revisions()}>
                {(revision, index) => (
                  <button
                    type="button"
                    class="den-file-edit-diff-step"
                    aria-pressed={activeIndex() === index()}
                    aria-label={revision.ariaLabel}
                    onClick={() => setActive(index())}
                  >
                    {revision.label}
                  </button>
                )}
              </For>
            </Scrollport>
          </Show>
          <div
            class="den-file-edit-diff-viewport"
            style={{ "min-height": reserved() }}
          >
            <Show
              when={!isNoop()}
              fallback={
                <p class="den-file-edit-diff-noop">{props.source.noopNote}</p>
              }
            >
              <Show
                when={wholeFile()}
                keyed
                fallback={
                  // Reader reuse keeps the current comparison visible during loading.
                  <ShowLatest when={access()} fallback={<p>Reconnect to read this comparison.</p>}>{reader =>
                    <SourceReader projectId={props.source.projectId} access={reader()} path={path()} wrap={inlineWrap()} onHandle={setReaderHandle} onReady={setReaderReady} find={false} scrollPastEnd={false} />
                  }</ShowLatest>
                }
              >
                {(whole) => <WholeFileDetails change={whole} path={path()} stat={stat()} onView={openFullscreen} />}
              </Show>
            </Show>
          </div>

          <footer class="den-file-edit-diff-footer">
            <button
              type="button"
              class="den-file-edit-diff-fullscreen-btn den-inset-icon-btn"
              aria-label="Full screen"
              onClick={openFullscreen}
            >
              <ThemeIcon slot="expand" size={14} />
            </button>
          </footer>
        </div>
      </Show>
    </article>
  );
}
