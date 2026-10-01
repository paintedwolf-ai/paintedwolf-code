import { For, Show, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { SourcePathLink } from "../../components/source/SourcePathLink.tsx";
import { SourceChangeChip } from "../../components/source/diff/SourceChangeChip.tsx";
import { LineStat, WholeFileDetails } from "../../components/source/diff/SourceDiffParts.tsx";
import { wholeFileChange } from "../../components/source/reader/source-reader-change.ts";
import { openDiffViewer } from "../../platform/navigation/in-app-diff.ts";
import type { DiffRowSource } from "../../components/source/diff/diff-row-source.ts";
import type { SourceComparisonSummary } from "../../api/types.ts";
import { SOURCE_COMPARISON_FAILURE } from "../../components/source/reader/SourceReader.tsx";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";

type Props = {
  source: DiffRowSource;
  open: boolean;
  onToggle: () => void;
  /** null reads the net range; an index reads one recorded write. */
  revision: number | null;
  onRevision: (revision: number | null) => void;
  reloading?: boolean;
};

function PathLink(props: { source: DiffRowSource; revision: number | null }) {
  return (
    <SourcePathLink
      truncate
      strong
      path={props.source.path(props.revision)}
      rootId={props.source.rootId(props.revision)}
      openable={props.source.openable(props.revision)}
      change={wholeFileChange(props.source.change(props.revision))}
      projectId={props.source.projectId}
      jobId={props.source.jobId}
      rootRefs={props.source.rootRefs}
      chatDestination={props.source.chatDestination}
    />
  );
}

export function DiffsSectionHeader(props: Props) {
  const revisions = createMemo(() => props.source.revisions);
  const hasRevisions = () => revisions().length > 1;
  const change = () => props.source.change(props.revision);
  const path = () => props.source.path(props.revision);
  const [hostStat, setHostStat] = createSignal<SourceComparisonSummary | null>(null);
  const [statError, setStatError] = createSignal<unknown>();
  const access = createMemo(() => props.source.access(props.revision));
  const stat = () => props.source.stat(props.revision) ?? hostStat();

  createEffect(() => {
    const reader = access();
    setHostStat(null);
    setStatError(undefined);
    if (!reader || props.revision === null) return;
    let cancelled = false;
    void reader.summary().then((summary) => {
      if (cancelled) return;
      setHostStat(summary);
    }).catch((cause) => {
      if (!cancelled) setStatError(cause);
    });
    onCleanup(() => { cancelled = true; });
  });

  observeSurfaceFailure(SOURCE_COMPARISON_FAILURE, statError, () => props.source.projectId);

  const isNoop = () => props.source.isNoop(props.revision);
  const isBinary = () => {
    if (props.source.isBinary?.(props.revision)) return true;
    const summary = hostStat();
    return summary?.before?.availability === "binary" || summary?.after?.availability === "binary";
  };
  const wholeFile = () => wholeFileChange(change());
  const removed = () => change() === "deleted" || props.source.openable(props.revision) === false;

  function openFullscreen(): void {
    const reader = access();
    if (reader) openDiffViewer({ projectId: props.source.projectId, path: path(), reader, change: change() });
  }

  return (
    <section
      class="den-diffs-section"
      data-testid="diffs-section-header"
      data-path={path()}
      data-expanded={props.open ? "true" : "false"}
      data-reloading={props.reloading ? "" : undefined}
    >
      <div class="den-diffs-section__band" onClick={props.onToggle}>
        <Show when={removed()} fallback={<PathLink source={props.source} revision={props.revision} />}>
          {/* A removed path has no file to open, so it opens the comparison. */}
          <button
            type="button"
            class="den-diffs-section__removed"
            aria-label={`Show the contents removed from ${path()}`}
            onClick={(event) => { event.stopPropagation(); openFullscreen(); }}
          >
            <PathLink source={props.source} revision={props.revision} />
          </button>
        </Show>
        <span class="den-file-edit-diff-fill" aria-hidden="true" />
        <SourceChangeChip change={change()} />
        <Show
          when={!isNoop() && !isBinary()}
          fallback={
            <span class="den-file-edit-diff-stat den-file-edit-diff-stat--noop" data-testid="file-edit-diff-stat">
              {isBinary() ? "binary" : "no net change"}
            </span>
          }
        >
          <Show keyed when={stat()} fallback={<Show when={!statError()}><span role="status">Comparing…</span></Show>}>
            {(value) => <LineStat stat={value} />}
          </Show>
        </Show>
        <Show when={hasRevisions()}>
          <span class="den-file-edit-diff-writes" data-testid="file-edit-diff-writes">
            {revisions().length} edits
          </span>
        </Show>
        <button
          type="button"
          class="den-file-edit-diff-fullscreen-btn den-inset-icon-btn"
          aria-label={`Full screen diff for ${path()}`}
          onClick={(event) => { event.stopPropagation(); openFullscreen(); }}
        >
          <ThemeIcon slot="expand" size={14} />
        </button>
        <button
          type="button"
          class="den-file-edit-diff-disclosure"
          aria-expanded={props.open}
          aria-label={`${props.open ? "Collapse" : "Show"} diff for ${path()}`}
          onClick={(event) => { event.stopPropagation(); props.onToggle(); }}
        >
          <span class="den-row-mark" aria-hidden="true" />
        </button>
      </div>
      <Show when={props.open && !wholeFile() && (isNoop() || isBinary())}>
        <p class="den-file-edit-diff-noop">{props.source.noopNote}</p>
      </Show>
      <Show when={props.open && hasRevisions()}>
        <div class="den-file-edit-diff-strip den-diffs-section__strip" data-testid="file-edit-diff-strip" data-reloading={props.reloading ? "" : undefined}>
          <div
            class="den-file-edit-diff-strip__content den-diffs-section__strip-content"
            role="group"
            aria-label={props.source.revisionsLabel}
          >
          <button
            type="button"
            class="den-file-edit-diff-step"
            aria-pressed={props.revision === null}
            onClick={() => props.onRevision(null)}
          >
            {props.source.netLabel}
          </button>
          <span class="den-file-edit-diff-step-sep" aria-hidden="true" />
          <For each={revisions()}>
            {(entry, index) => (
              <button
                type="button"
                class="den-file-edit-diff-step"
                aria-pressed={props.revision === index()}
                aria-label={entry.ariaLabel}
                onClick={() => props.onRevision(index())}
              >
                {entry.label}
              </button>
            )}
          </For>
          </div>
        </div>
      </Show>
      <Show when={props.open && !isNoop() && !isBinary() && wholeFile()} keyed>
        {(whole) => (
          <WholeFileDetails class="den-diffs-section__details" change={whole} path={path()} stat={stat()} onView={openFullscreen} />
        )}
      </Show>
    </section>
  );
}
