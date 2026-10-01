import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type {
  CoordinatorBatchPhase,
  ProgressChange,
  ProgressState,
  ProgressStep,
  ProgressUpdateSummary,
} from "../api/types.ts";
import { BATCH_PHASE_LABELS } from "../chat/progress/batch-phase-copy.generated.ts";
import { turnClockElapsedMs } from "../chat/session/turn-clock.ts";
import { useNow } from "../time/now.ts";
import { formatDuration } from "../time/time-copy.ts";
import {
  isTerminalProgressStep,
  progressChangesToSteps,
  progressUpdateSummaryLabel,
  visibleBatchPhase,
} from "../chat/progress/progress-model.ts";
import { useTranscriptEntry } from "../chat/transcript/presentation/transcript-entry.ts";
import { ContextRing } from "./chatview/ContextRing.tsx";
import { MarkdownBody } from "./transcript/MarkdownBody.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { chromeProps } from "../styling/ui-chrome.ts";

export type ProgressStripProps = {
  batchPhase?: CoordinatorBatchPhase;
  steps?: readonly ProgressStep[];
  onOpenWorklog?: () => void;
  worklogOpen?: boolean;
  /** Finished transcript snapshot. */
  completed?: boolean;
  /** Newly authored transcript snapshot. */
  created?: boolean;
  /** Mid-run transcript changes. */
  deltaChanges?: readonly ProgressChange[];
  /** Aggregate transcript update. */
  summary?: ProgressUpdateSummary;
  /** Banked model time in milliseconds. */
  activeMs?: number;
  running?: boolean;
  /** RFC 3339 clock-resume instant. */
  runningSince?: string;
  /** Current prompt-token count. */
  contextPrompt?: number;
  /** Model context-window size. */
  contextWindow?: number;
  /** Prompt-token compaction threshold. */
  compactionThreshold?: number;
  embedded?: boolean;
  /** Whether the transcript contains the completion row. */
  archivedInTranscript?: boolean;
  sessionId?: string;
  entryKey?: string;
};

function stateGlyph(state: ProgressState): string {
  switch (state) {
    case "done":
      return "✓";
    case "na":
      return "–";
    default:
      return "○";
  }
}

type DisplayRow = ProgressStep & { leaving: boolean };

function splitProgressColumns<T>(items: readonly T[]): [T[], T[]] {
  if (items.length === 0) return [[], []];
  const mid = Math.ceil(items.length / 2);
  return [items.slice(0, mid), items.slice(mid)];
}

function PlanRow(props: { item: DisplayRow; embedded?: boolean }) {
  return (
    <li
      class="progress-strip__item"
      classList={{
        "progress-strip__item--done": props.item.state === "done",
        "progress-strip__item--na": props.item.state === "na",
        "progress-strip__item--leaving": !props.embedded && props.item.leaving,
      }}
      data-testid="progress-strip-item"
      data-kind="plan"
      data-state={props.item.state}
    >
      <span class="progress-strip__glyph" aria-hidden="true">
        {stateGlyph(props.item.state)}
      </span>
      <div class="progress-strip__label">
        <MarkdownBody source={props.item.label} />
      </div>
    </li>
  );
}

/** Coordinator phase and checklist. */
export function ProgressStrip(props: ProgressStripProps) {
  const [collapsed, setCollapsed] = createSignal(false);
  const [bodyPull, setBodyPull] = createSignal(false);
  const transcriptEntryKey = () => props.entryKey?.trim();
  const { bindTranscriptEntry } = useTranscriptEntry(() =>
    props.sessionId && transcriptEntryKey()
      ? { sessionId: props.sessionId, entryKey: transcriptEntryKey() }
      : undefined,
  );

  const readOnly = () =>
    props.completed === true ||
    props.created === true ||
    props.summary != null ||
    (props.deltaChanges?.length ?? 0) > 0;
  const nowMs = useNow("second", () => !readOnly());
  const elapsedMs = () =>
    turnClockElapsedMs(
      {
        active_ms: props.activeMs ?? 0,
        running: props.running === true,
        running_at: props.runningSince,
      },
      nowMs(),
    );
  const showElapsed = () => !readOnly() && (props.running || elapsedMs() > 0);

  const steps = () => {
    if (props.summary) {
      return [{
        label: progressUpdateSummaryLabel(props.summary, props.created === true),
        state: props.summary.pending > 0 ? "pending" : "done",
      }] satisfies ProgressStep[];
    }
    if (props.deltaChanges?.length) {
      return progressChangesToSteps(props.deltaChanges);
    }
    return props.steps ?? [];
  };
  const phaseChip = () => (readOnly() ? undefined : visibleBatchPhase(props.batchPhase));

  const ITEM_EXIT_MS = 240;
  const [rows, setRows] = createSignal<DisplayRow[]>(
    steps().map((s) => ({ ...s, leaving: false })),
  );
  const exitTimers = new Map<string, ReturnType<typeof setTimeout>>();
  // Keep terminal rows until transcript archival.
  const retainTerminalRows = () => props.archivedInTranscript !== true;
  // Preserve snapshot height during rebuild gaps.
  const itemCount = () =>
    props.summary
      ? props.created
        ? props.summary.total_steps
        : props.summary.change_count
      : readOnly()
        ? steps().length || rows().length
        : steps().filter((s) => !isTerminalProgressStep(s.state)).length;
  createEffect(() => {
    const incoming = steps();
    if (readOnly()) {
      // Snapshots update without exit animation.
      if (incoming.length === 0) return;
      for (const timer of exitTimers.values()) clearTimeout(timer);
      exitTimers.clear();
      setRows((prev) => {
        const prevByLabel = new Map(prev.map((row) => [row.label, row]));
        return incoming.map((step) => {
          const match = prevByLabel.get(step.label);
          return match && !match.leaving && match.state === step.state
            ? match
            : { ...step, leaving: false };
        });
      });
      return;
    }
    const incomingByLabel = new Map(incoming.map((s) => [s.label, s]));
    setRows((prev) => {
      const result: DisplayRow[] = [];
      const seen = new Set<string>();
      for (const row of prev) {
        if (seen.has(row.label)) continue;
        seen.add(row.label);
        const live = incomingByLabel.get(row.label);
        if (
          live &&
          (retainTerminalRows() || !isTerminalProgressStep(live.state))
        ) {
          const t = exitTimers.get(row.label);
          if (t) {
            clearTimeout(t);
            exitTimers.delete(row.label);
          }
          if (!row.leaving && row.state === live.state) {
            result.push(row);
          } else {
            result.push({ ...live, leaving: false });
          }
        } else if (props.embedded) {
          const t = exitTimers.get(row.label);
          if (t) {
            clearTimeout(t);
            exitTimers.delete(row.label);
          }
        } else {
          const departing = live ?? row;
          if (!exitTimers.has(row.label)) {
            exitTimers.set(
              row.label,
              setTimeout(() => {
                exitTimers.delete(row.label);
                setRows((cur) => cur.filter((r) => r.label !== row.label));
              }, ITEM_EXIT_MS),
            );
          }
          result.push({ ...departing, leaving: true });
        }
      }
      for (const s of incoming) {
        if (
          !seen.has(s.label) &&
          (retainTerminalRows() || !isTerminalProgressStep(s.state))
        ) {
          seen.add(s.label);
          result.push({ ...s, leaving: false });
        }
      }
      return result;
    });
  });
  onCleanup(() => {
    for (const t of exitTimers.values()) clearTimeout(t);
  });

  const showStrip = () =>
    readOnly()
      ? itemCount() > 0
      : props.embedded || phaseChip() != null || rows().length > 0;
  const bodyOpen = () =>
    readOnly() ? true : props.embedded ? true : !collapsed();
  const embeddedColumns = () => splitProgressColumns(rows());

  let listScrollEl: HTMLDivElement | undefined;
  let columnsScrollEl: HTMLDivElement | undefined;
  createEffect((wasOpen: boolean) => {
    const open = bodyOpen();
    if (open && !wasOpen && !readOnly()) {
      requestAnimationFrame(() => {
        const scrollEl = columnsScrollEl ?? listScrollEl;
        if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
      });
    }
    return open;
  }, false);

  return (
    <Show when={showStrip()}>
      <section
        ref={props.sessionId && transcriptEntryKey() ? bindTranscriptEntry : undefined}
        class="progress-strip"
        classList={{
          "progress-strip--collapsed": !readOnly() && collapsed(),
          "progress-strip--snapshot": readOnly(),
          "progress-strip--embedded": props.embedded === true,
        }}
        data-testid={
          props.completed
            ? "progress-strip-completed"
            : props.created
              ? "progress-strip-created"
              : props.summary != null || (props.deltaChanges?.length ?? 0) > 0
                ? "progress-strip-delta"
                : "progress-strip"
        }
      >
        <Show
          when={!readOnly()}
          fallback={
            <header class="progress-strip__header" {...chromeProps()}>
              <span class="progress-strip__title">
                {props.created
                  ? "Progress"
                  : props.summary != null || (props.deltaChanges?.length ?? 0) > 0
                    ? "Updated"
                    : "Done"}
              </span>
              <span
                class="progress-strip__count"
                data-testid="progress-strip-count"
              >
                {itemCount()}
              </span>
            </header>
          }
        >
          <header class="progress-strip__header" {...chromeProps()}>
            <Show when={props.onOpenWorklog}>
              <button
                type="button"
                class="den-inline-control"
                data-tone="accent"
                data-testid="worklog-open"
                aria-haspopup="dialog"
                aria-expanded={props.worklogOpen === true}
                aria-label="Open worklog"
                onClick={() => props.onOpenWorklog?.()}
              >
                Worklog
              </button>
            </Show>
            <Show when={phaseChip()}>
              {(phase) => (
                <span
                  class="den-status-mark"
                  data-testid="progress-batch-phase"
                  data-phase={phase()}
                  data-tone="accent"
                >
                  {BATCH_PHASE_LABELS[phase()]}
                </span>
              )}
            </Show>
            <Show when={!props.embedded}>
              <button
                type="button"
                class="progress-strip__toggle"
                aria-expanded={!collapsed()}
                aria-label={collapsed() ? "Expand progress" : "Collapse progress"}
                data-testid="progress-strip-toggle"
                onClick={() => {
                  setCollapsed((c) => {
                    if (c) setBodyPull(true);
                    return !c;
                  });
                }}
              >
                <span
                  class="progress-strip__caret den-tool-chicklet-caret"
                  aria-hidden="true"
                />
                <span class="progress-strip__title">Progress</span>
                <span
                  class="progress-strip__count"
                  data-testid="progress-strip-count"
                >
                  {itemCount()}
                </span>
              </button>
            </Show>
            <Show when={showElapsed()}>
              <span
                class="progress-strip__elapsed"
                data-testid="progress-strip-elapsed"
              >
                {formatDuration(elapsedMs())}
              </span>
            </Show>
            <ContextRing
              prompt={props.contextPrompt}
              window={props.contextWindow}
              compactionThreshold={props.compactionThreshold}
              testId="progress-context-meter"
            />
          </header>
        </Show>
        <Show when={props.embedded ? true : bodyOpen() && rows().length > 0}>
          <div
            class="progress-strip__body"
            classList={{ "progress-strip__body--pull": bodyPull() }}
            onAnimationEnd={(event) => {
              if (event.animationName === "progress-strip-pull") {
                setBodyPull(false);
              }
            }}
          >
            <Show
              when={rows().length > 0}
              fallback={
                <Show when={props.embedded}>
                  <p class="progress-strip__empty" role="status">
                    No active plan yet.
                  </p>
                </Show>
              }
            >
              <Show
                when={props.embedded}
                fallback={
                  <Show
                    when={readOnly()}
                    fallback={
                      <Scrollport
                        class="progress-strip__list-scroll"
                        contentAs="ul"
                        contentClass="progress-strip__list"
                        viewportRef={(el) => { listScrollEl = el; }}
                      >
                        <For each={rows()}>
                          {(item) => <PlanRow item={item} />}
                        </For>
                      </Scrollport>
                    }
                  >
                    <ul class="progress-strip__list">
                      <For each={rows()}>
                        {(item) => <PlanRow item={item} />}
                      </For>
                    </ul>
                  </Show>
                }
              >
                <Scrollport
                  class="progress-strip__columns"
                  contentClass="progress-strip__columns-content"
                  viewportRef={(el) => { columnsScrollEl = el; }}
                  data-testid="progress-strip-columns"
                >
                  <ul class="progress-strip__column">
                    <For each={embeddedColumns()[0]}>
                      {(item) => <PlanRow item={item} embedded />}
                    </For>
                  </ul>
                  <ul class="progress-strip__column">
                    <For each={embeddedColumns()[1]}>
                      {(item) => <PlanRow item={item} embedded />}
                    </For>
                  </ul>
                </Scrollport>
              </Show>
            </Show>
          </div>
        </Show>
      </section>
    </Show>
  );
}
