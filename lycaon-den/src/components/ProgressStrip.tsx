import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import type { CoordinatorBatchPhase, ProgressStep } from "../api/types.ts";
import { BATCH_PHASE_LABELS } from "../chat/progress/batch-phase-copy.generated.ts";
import { turnClockElapsedMs } from "../chat/session/turn-clock.ts";
import { useNow } from "../time/now.ts";
import { formatDuration } from "../time/time-copy.ts";
import {
  isTerminalProgressStep,
  visibleBatchPhase,
} from "../chat/progress/progress-model.ts";
import { ContextRing } from "./chatview/ContextRing.tsx";
import { ProgressStepRow } from "./ProgressStepRow.tsx";
import { Scrollport } from "./primitives/Scrollport.tsx";
import { chromeProps } from "../styling/ui-chrome.ts";

export type ProgressStripProps = {
  batchPhase?: CoordinatorBatchPhase;
  steps?: readonly ProgressStep[];
  onOpenWorklog?: () => void;
  worklogOpen?: boolean;
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
};

type DisplayRow = ProgressStep & { leaving: boolean };

function splitProgressColumns<T>(items: readonly T[]): [T[], T[]] {
  if (items.length === 0) return [[], []];
  const mid = Math.ceil(items.length / 2);
  return [items.slice(0, mid), items.slice(mid)];
}

/** The live coordinator phase and checklist. */
export function ProgressStrip(props: ProgressStripProps) {
  const [collapsed, setCollapsed] = createSignal(false);
  const [bodyPull, setBodyPull] = createSignal(false);

  const nowMs = useNow("second", () => props.running === true);
  const elapsedMs = () =>
    turnClockElapsedMs(
      {
        active_ms: props.activeMs ?? 0,
        running: props.running === true,
        running_at: props.runningSince,
      },
      nowMs(),
    );
  const showElapsed = () => props.running || elapsedMs() > 0;

  const steps = () => props.steps ?? [];
  const phaseChip = () => visibleBatchPhase(props.batchPhase);

  const ITEM_EXIT_MS = 240;
  const [rows, setRows] = createSignal<DisplayRow[]>(
    steps().map((s) => ({ ...s, leaving: false })),
  );
  const exitTimers = new Map<string, ReturnType<typeof setTimeout>>();
  // Keep terminal rows until transcript archival.
  const retainTerminalRows = () => props.archivedInTranscript !== true;
  const itemCount = () =>
    steps().filter((s) => !isTerminalProgressStep(s.state)).length;
  createEffect(() => {
    const incoming = steps();
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
    props.embedded || phaseChip() != null || rows().length > 0;
  const bodyOpen = () => (props.embedded ? true : !collapsed());
  const embeddedColumns = () => splitProgressColumns(rows());

  let listScrollEl: HTMLDivElement | undefined;
  let columnsScrollEl: HTMLDivElement | undefined;
  createEffect((wasOpen: boolean) => {
    const open = bodyOpen();
    if (open && !wasOpen) {
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
        class="progress-strip"
        classList={{
          "progress-strip--collapsed": collapsed(),
          "progress-strip--embedded": props.embedded === true,
        }}
        data-testid="progress-strip"
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
                  <Scrollport
                    class="progress-strip__list-scroll"
                    contentAs="ul"
                    contentClass="progress-strip__list"
                    viewportRef={(el) => { listScrollEl = el; }}
                  >
                    <For each={rows()}>
                      {(item) => <ProgressStepRow step={item} leaving={item.leaving} />}
                    </For>
                  </Scrollport>
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
                      {(item) => <ProgressStepRow step={item} />}
                    </For>
                  </ul>
                  <ul class="progress-strip__column">
                    <For each={embeddedColumns()[1]}>
                      {(item) => <ProgressStepRow step={item} />}
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
