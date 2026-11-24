import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { StandingWatch } from "../../api/types.ts";
import {
  formatHostWatchError,
  formatStandingStateLabel,
  formatWatchConditionsSummary,
} from "../../watches/watch-format.ts";
import {
  formatRunInTimeZone,
  type StandingWatchEditorSeed,
} from "../../watches/watch-schedule-preview.ts";
import {
  deleteProjectStandingWatchAction,
  pauseProjectStandingWatchAction,
  resumeProjectStandingWatchAction,
  runProjectStandingWatchNowAction,
} from "../../watches/watches-actions.ts";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import type { ContextMenuItem } from "../ContextMenu.tsx";
import { StandingWatchEditor } from "./StandingWatchEditor.tsx";
import { StandingWatchRunHistory } from "./StandingWatchRunHistory.tsx";

export function seedFromExistingStandingWatch(
  w: StandingWatch,
): StandingWatchEditorSeed {
  return {
    mode: "edit",
    watchId: w.watch_id,
    revision: w.revision,
    name: w.name,
    prompt: w.prompt,
    conditions: w.conditions.map((c) => ({ ...c })),
    gate: w.gate ? { ...w.gate } : null,
    minGapMinutes: w.min_gap_minutes,
    targetKind: w.target.kind,
    targetSessionId: w.target.session_id ?? "",
    workflowId: w.workflow_id,
    modelId: w.model_id,
    runSpendLimitNanoUsd: w.run_spend_limit_nano_usd,
    allowUnpriced: w.allow_unpriced,
    dailyRunCap: w.daily_run_cap,
    timeZone: w.time_zone,
    enableDisabledTakeover: w.state === "disabled",
    changedFields: new Set(),
  };
}

type Props = {
  projectId: string;
  standingWatches: readonly StandingWatch[];
  selectedWatchId: string | null;
  editorSeed: StandingWatchEditorSeed | null;
  client: () => LycaonClient | null;
  isDisconnected: boolean;
  onSelectWatch: (watchId: string | null) => void;
  onOpenEditorSeed: (seed: StandingWatchEditorSeed | null) => void;
  onOpenSession: (sessionId: string) => void;
};

export function StandingWatchesSection(props: Props) {
  const [busyId, setBusyId] = createSignal<string | null>(null);
  const [rowErrors, setRowErrors] = createSignal<ReadonlyMap<string, string>>(
    new Map(),
  );

  const setRowError = (watchId: string, msg: string | null) => {
    setRowErrors((prev) => {
      const next = new Map(prev);
      if (msg) next.set(watchId, msg);
      else next.delete(watchId);
      return next;
    });
  };

  const runRowAction = async (
    watchId: string,
    fn: (c: LycaonClient) => Promise<unknown>,
  ) => {
    const client = props.client();
    if (!client || busyId() === watchId) return;
    setBusyId(watchId);
    setRowError(watchId, null);
    try {
      await fn(client);
    } catch (err) {
      setRowError(watchId, formatHostWatchError(err));
    } finally {
      setBusyId(null);
    }
  };

  const menuItemsFor = (w: StandingWatch): ContextMenuItem[] => {
    const disabled = props.isDisconnected || busyId() === w.watch_id;
    const items: ContextMenuItem[] = [];
    if (w.state === "enabled" || w.state === "paused") {
      items.push({
        label: "Run now",
        disabled,
        testId: `standing-watch-run-now-${w.watch_id}`,
        onSelect: () =>
          void runRowAction(w.watch_id, (c) =>
            runProjectStandingWatchNowAction(c, props.projectId, w.watch_id),
          ),
      });
    } else {
      items.push({
        label: "Run now",
        disabled: true,
        testId: `standing-watch-run-now-${w.watch_id}`,
      });
    }

    if (w.state === "enabled") {
      items.push({
        label: "Pause",
        disabled,
        testId: `standing-watch-pause-${w.watch_id}`,
        onSelect: () =>
          void runRowAction(w.watch_id, (c) =>
            pauseProjectStandingWatchAction(
              c,
              props.projectId,
              w.watch_id,
              w.revision,
            ),
          ),
      });
    } else if (w.state === "paused") {
      items.push({
        label: "Resume",
        disabled,
        testId: `standing-watch-resume-${w.watch_id}`,
        onSelect: () =>
          void runRowAction(w.watch_id, (c) =>
            resumeProjectStandingWatchAction(
              c,
              props.projectId,
              w.watch_id,
              w.revision,
            ),
          ),
      });
    }

    items.push({
      label: "Edit",
      disabled,
      testId: `standing-watch-edit-${w.watch_id}`,
      onSelect: () => {
        props.onSelectWatch(w.watch_id);
        props.onOpenEditorSeed(seedFromExistingStandingWatch(w));
      },
    });

    items.push({
      label: "Delete",
      danger: true,
      disabled,
      testId: `standing-watch-delete-${w.watch_id}`,
      onSelect: () =>
        void runRowAction(w.watch_id, async (c) => {
          await deleteProjectStandingWatchAction(c, props.projectId, w.watch_id);
          if (props.selectedWatchId === w.watch_id) {
            props.onSelectWatch(null);
            props.onOpenEditorSeed(null);
          }
        }),
    });

    return items;
  };

  const selectedWatch = () =>
    props.standingWatches.find((w) => w.watch_id === props.selectedWatchId);

  const standaloneEditor = () =>
    props.editorSeed &&
    (props.editorSeed.mode === "create" ||
      (props.editorSeed.mode === "keep" && !props.editorSeed.watchId));

  return (
    <Show when={props.standingWatches.length > 0 || props.editorSeed != null}>
      <section
        class="flex flex-col gap-den-3"
        aria-label="Standing watches"
        data-testid="activity-standing-watches-section"
      >
        <h2 class="text-den-body font-medium text-den-text-primary">
          Standing watches
        </h2>

        <Show when={standaloneEditor() && props.editorSeed} keyed>
          {(seed) => (
            <StandingWatchEditor
              projectId={props.projectId}
              seed={seed}
              client={props.client}
              onSaved={(saved) => {
                props.onOpenEditorSeed(null);
                props.onSelectWatch(saved.watch_id);
              }}
              onCancel={() => props.onOpenEditorSeed(null)}
            />
          )}
        </Show>

        <Show when={props.standingWatches.length > 0}>
          <ul
            class="flex flex-col gap-den-2"
            data-testid="standing-watches-list"
          >
            <For each={props.standingWatches}>
              {(w) => {
                const isSelected = () => props.selectedWatchId === w.watch_id;
                const nextOrState = () => {
                  if (w.state === "enabled" && w.next_run_at) {
                    const ms = Date.parse(w.next_run_at);
                    if (!Number.isNaN(ms)) {
                      return `Next run ${formatRunInTimeZone(ms, w.time_zone)}`;
                    }
                  }
                  return formatStandingStateLabel(w.state, w.state_reason);
                };
                const summary = () =>
                  formatWatchConditionsSummary(w.conditions, {
                    timeZone: w.time_zone,
                    repeat: true,
                  });
                const errText = () => rowErrors().get(w.watch_id) ?? null;

                return (
                  <li
                    class="flex flex-col gap-den-2 rounded-den-md border bg-den-bg-surface p-den-3"
                    classList={{
                      "border-den-accent": isSelected(),
                      "border-den-line": !isSelected(),
                    }}
                    data-testid={`standing-watch-row-${w.watch_id}`}
                  >
                    <div class="flex items-start justify-between gap-den-2">
                      <button
                        type="button"
                        class="flex flex-1 flex-col gap-den-1 text-left"
                        aria-expanded={isSelected()}
                        data-testid={`standing-watch-select-${w.watch_id}`}
                        onClick={() => {
                          if (isSelected()) {
                            props.onSelectWatch(null);
                            props.onOpenEditorSeed(null);
                          } else {
                            props.onSelectWatch(w.watch_id);
                            props.onOpenEditorSeed(
                              seedFromExistingStandingWatch(w),
                            );
                          }
                        }}
                      >
                        <span
                          class="text-den-body font-medium text-den-text-primary"
                          data-testid={`standing-watch-name-${w.watch_id}`}
                        >
                          {w.name}
                        </span>
                        <span class="flex flex-wrap items-center gap-den-2 text-den-caption text-den-text-muted">
                          <span data-testid={`standing-watch-summary-${w.watch_id}`}>
                            {summary()}
                          </span>
                          <span>·</span>
                          <span data-testid={`standing-watch-state-${w.watch_id}`}>
                            {nextOrState()}
                          </span>
                          <Show when={w.last_outcome}>
                            <span>· Last: {w.last_outcome}</span>
                          </Show>
                          <span>· Owner: {w.owner_person_id}</span>
                        </span>
                      </button>

                      <BrowseOverflowMenu
                        testId={`standing-watch-menu-${w.watch_id}`}
                        label={`Actions for ${w.name}`}
                        variant="icon"
                        items={menuItemsFor(w)}
                      />
                    </div>

                    <Show when={errText()}>
                      {(msg) => (
                        <p
                          role="alert"
                          class="text-den-caption text-den-err"
                          data-testid={`standing-watch-row-error-${w.watch_id}`}
                        >
                          {msg()}
                        </p>
                      )}
                    </Show>

                    <Show when={isSelected() && selectedWatch()} keyed>
                      {(detailWatch) => (
                        <div
                          class="flex flex-col gap-den-3 border-t border-den-line pt-den-3"
                          data-testid={`standing-watch-detail-${w.watch_id}`}
                        >
                          <StandingWatchEditor
                            projectId={props.projectId}
                            seed={
                              props.editorSeed?.watchId === detailWatch.watch_id
                                ? props.editorSeed
                                : seedFromExistingStandingWatch(detailWatch)
                            }
                            existingWatch={detailWatch}
                            client={props.client}
                            onSaved={() => props.onOpenEditorSeed(null)}
                            onCancel={() => {
                              props.onSelectWatch(null);
                              props.onOpenEditorSeed(null);
                            }}
                          />
                          <StandingWatchRunHistory
                            projectId={props.projectId}
                            watch={detailWatch}
                            client={props.client}
                            onOpenSession={props.onOpenSession}
                          />
                        </div>
                      )}
                    </Show>
                  </li>
                );
              }}
            </For>
          </ul>
        </Show>
      </section>
    </Show>
  );
}
