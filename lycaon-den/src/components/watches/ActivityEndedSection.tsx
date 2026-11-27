import { For, Show, createMemo, createSignal } from "solid-js";
import type { ProjectBackgroundProcess, WatchRun } from "../../api/types.ts";
import { sessionTitle } from "../../chat/session/session-title.ts";
import { formatBackgroundTerminationReason } from "../../project/background-termination-reason.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { formatWatchRunStatus } from "../../watches/watch-format.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";

type Props = {
  endedProcesses: readonly ProjectBackgroundProcess[];
  endedRuns: readonly WatchRun[];
  onOpenSession: (sessionId: string) => void;
};

function formatTimeLabel(iso: string): string {
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? "—" : formatRelativeTime(ms);
}

/** Section 3 of Activity: recently ended background commands plus standing-watch runs in the last 24h. */
export function ActivityEndedSection(props: Props) {
  const [endedExpanded, setEndedExpanded] = createSignal(false);

  const endedProcessIds = createMemo(() =>
    props.endedProcesses.map((p) => p.process_id),
  );
  const endedProcessById = (processId: string) =>
    props.endedProcesses.find((p) => p.process_id === processId);

  const totalCount = () =>
    props.endedProcesses.length + props.endedRuns.length;

  return (
    <Show when={totalCount() > 0}>
      <section
        class="project-running-ended flex flex-col gap-den-2 border-t border-den-line pt-den-3"
        data-testid="project-running-ended-section"
        aria-label="Recently ended"
      >
        <button
          type="button"
          class="project-running-ended__toggle flex items-center gap-den-2 text-left text-den-body font-medium text-den-text-muted"
          aria-expanded={endedExpanded()}
          data-testid="project-running-ended-toggle"
          onClick={() => setEndedExpanded((prev) => !prev)}
        >
          <span>Recently ended ({totalCount()})</span>
        </button>

        <Show when={endedExpanded()}>
          <ul
            class="project-running-ended__list flex flex-col gap-den-2"
            data-testid="project-running-ended-list"
          >
            <For each={props.endedRuns}>
              {(run) => (
                <li
                  class="project-running-ended__row flex flex-col gap-den-1 rounded-den-sm border border-den-line bg-den-bg-surface p-den-2-5"
                  data-testid={`activity-ended-run-${run.run_id}`}
                >
                  <div class="flex items-start justify-between gap-den-3">
                    <span class="text-den-body font-medium text-den-text-primary">
                      {run.watch_name}
                    </span>
                    <Show when={run.session_id} keyed>
                      {(sid) => (
                        <DenButton
                          variant="ghost"
                          compact
                          data-testid={`activity-ended-run-open-chat-${run.run_id}`}
                          onClick={() => props.onOpenSession(sid)}
                        >
                          Open chat
                        </DenButton>
                      )}
                    </Show>
                  </div>
                  <div class="flex flex-wrap items-center gap-den-3 text-den-caption text-den-text-muted">
                    <span>{formatWatchRunStatus(run.status)}</span>
                    <Show when={run.ended_at} keyed>
                      {(endedAt) => (
                        <span
                          data-tip={viewerCalendarTimeFormat().fullDateTime(
                            Date.parse(endedAt),
                          )}
                        >
                          Ended {formatTimeLabel(endedAt)}
                        </span>
                      )}
                    </Show>
                  </div>
                </li>
              )}
            </For>

            <For each={endedProcessIds()}>
              {(processId) => {
                const proc = () => endedProcessById(processId);
                return (
                  <ShowLatest when={proc()}>
                    {(item) => (
                      <li
                        class="project-running-ended__row flex flex-col gap-den-1 rounded-den-sm border border-den-line bg-den-bg-surface p-den-2-5"
                        data-testid={`project-running-ended-row-${processId}`}
                      >
                        <div class="flex items-start justify-between gap-den-3">
                          <code
                            class="font-mono text-den-body text-den-text-primary break-all"
                            data-testid={`project-running-ended-command-${processId}`}
                          >
                            {item().command}
                          </code>
                          <DenButton
                            variant="ghost"
                            compact
                            data-testid={`project-running-ended-open-chat-${processId}`}
                            onClick={() =>
                              props.onOpenSession(item().session_id)
                            }
                          >
                            Open chat
                          </DenButton>
                        </div>

                        <div class="flex flex-wrap items-center gap-den-3 text-den-caption text-den-text-muted">
                          <span
                            data-testid={`project-running-ended-reason-${processId}`}
                          >
                            {formatBackgroundTerminationReason(
                              item().termination_reason,
                            )}
                          </span>
                          <span
                            data-testid={`project-running-ended-chat-${processId}`}
                          >
                            {sessionTitle(item().session_title)}
                          </span>
                          <Show when={item().session_archived}>
                            <span
                              class="rounded-den-sm border border-den-line px-den-1-5 py-den-0-5 text-den-caption text-den-text-muted"
                              data-testid={`project-running-ended-archived-${processId}`}
                            >
                              Archived
                            </span>
                          </Show>
                          <Show when={item().working_dir}>
                            <span class="font-mono">{item().working_dir}</span>
                          </Show>
                          <Show when={item().ended_at}>
                            {(endedAt) => (
                              <span
                                data-tip={viewerCalendarTimeFormat().fullDateTime(
                                  Date.parse(endedAt()),
                                )}
                              >
                                Ended {formatTimeLabel(endedAt())}
                              </span>
                            )}
                          </Show>
                        </div>
                      </li>
                    )}
                  </ShowLatest>
                );
              }}
            </For>
          </ul>
        </Show>
      </section>
    </Show>
  );
}
