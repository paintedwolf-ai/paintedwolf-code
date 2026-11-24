import { For, Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  AgentWatch,
  ProjectBackgroundProcess,
  StandingWatch,
  WatchRun,
} from "../../api/types.ts";
import { sessionTitle } from "../../chat/session/session-title.ts";
import type { ProjectBackgroundChatGroup } from "../../project/project-background-store.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { formatHostWatchError } from "../../watches/watch-format.ts";
import {
  cancelSessionWatchAction,
  keepSessionWatchAction,
} from "../../watches/watches-actions.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";

type Props = {
  runningGroups: readonly ProjectBackgroundChatGroup[];
  liveAgentWatches: readonly AgentWatch[];
  activeRunBySession: ReadonlyMap<
    string,
    { watch: StandingWatch; run: WatchRun }
  >;
  standingById: ReadonlyMap<string, StandingWatch>;
  queuedRuns: readonly WatchRun[];
  highlightedSessionId: string | null;
  isDisconnected: boolean;
  isStopping: (processId: string) => boolean;
  stopError: (processId: string) => string | null;
  client: () => LycaonClient | null;
  onStopProcess: (proc: ProjectBackgroundProcess) => void;
  onOpenSession: (sessionId: string) => void;
  onSelectStandingWatch: (watchId: string) => void;
  onPrefillFromAgentWatch: (watch: AgentWatch) => void;
};

function formatTimeLabel(iso: string): string {
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? "—" : formatRelativeTime(ms);
}

function formatPortsLabel(ports: readonly string[]): string {
  if (ports.length === 0) return "";
  if (ports.length === 1) return `Port ${ports[0]}`;
  return `Ports ${ports.join(", ")}`;
}

/** Section 2 of Activity: live background commands, agent watches, active standing-watch runs, and queued runs. */
export function ActivityLiveSection(props: Props) {
  const [busyWatchKey, setBusyWatchKey] = createSignal<string | null>(null);
  const [watchErrors, setWatchErrors] = createSignal<ReadonlyMap<string, string>>(
    new Map(),
  );

  const setWatchErr = (key: string, msg: string | null) => {
    setWatchErrors((prev) => {
      const next = new Map(prev);
      if (msg) next.set(key, msg);
      else next.delete(key);
      return next;
    });
  };

  const runWatchAction = async (
    key: string,
    fn: (c: LycaonClient) => Promise<unknown>,
  ) => {
    const client = props.client();
    if (!client || busyWatchKey() === key) return;
    setBusyWatchKey(key);
    setWatchErr(key, null);
    try {
      await fn(client);
    } catch (err) {
      setWatchErr(key, formatHostWatchError(err));
    } finally {
      setBusyWatchKey(null);
    }
  };

  const sessionIds = createMemo(() => {
    const ordered: string[] = [];
    const seen = new Set<string>();
    const add = (sid: string) => {
      if (sid && !seen.has(sid)) {
        seen.add(sid);
        ordered.push(sid);
      }
    };
    for (const g of props.runningGroups) add(g.sessionId);
    for (const sid of props.activeRunBySession.keys()) add(sid);
    for (const w of props.liveAgentWatches) add(w.session_id);
    return ordered;
  });

  const groupForSession = (sessionId: string) =>
    props.runningGroups.find((g) => g.sessionId === sessionId);

  const watchesForSession = (sessionId: string) =>
    props.liveAgentWatches.filter((w) => w.session_id === sessionId);

  const hasAnyLive = () =>
    sessionIds().length > 0 || props.queuedRuns.length > 0;

  const renderAgentWatchRow = (w: AgentWatch, nested: boolean) => {
    const key = `${w.session_id}:${w.handle}`;
    const busy = () => busyWatchKey() === key || props.isDisconnected;
    const errText = () => watchErrors().get(key) ?? null;
    const draft = w.standing_draft;
    const replacedRev = () =>
      draft?.replaces
        ? props.standingById.get(draft.replaces)?.revision
        : undefined;

    return (
      <li
        class="flex flex-col gap-den-1-5 rounded-den-sm border border-den-line p-den-2"
        classList={{ "ml-den-4 bg-den-bg-base": nested }}
        data-testid={`activity-agent-watch-${w.handle}`}
      >
        <div class="flex flex-wrap items-start justify-between gap-den-2">
          <div class="flex flex-col gap-den-0-5">
            <span class="text-den-body font-medium text-den-text-primary">
              {draft
                ? `Proposal: ${draft.name}`
                : nested
                  ? `Derived watch · ${w.handle}`
                  : `Watch · ${w.handle}`}
            </span>
            <span class="text-den-caption text-den-text-muted">
              {w.condition_summary}
            </span>
          </div>

          <div class="flex flex-wrap items-center gap-den-2">
            <Show
              when={draft != null}
              fallback={
                <>
                  <Show when={w.keepable}>
                    <DenButton
                      variant="secondary"
                      compact
                      disabled={busy()}
                      data-testid={`activity-watch-keep-${w.handle}`}
                      onClick={() => props.onPrefillFromAgentWatch(w)}
                    >
                      Keep
                    </DenButton>
                  </Show>
                  <DenButton
                    variant="secondary"
                    compact
                    disabled={busy()}
                    data-testid={`activity-watch-cancel-${w.handle}`}
                    onClick={() =>
                      void runWatchAction(key, (c) =>
                        cancelSessionWatchAction(c, w.session_id, w.handle),
                      )
                    }
                  >
                    Cancel watch
                  </DenButton>
                </>
              }
            >
              <DenButton
                variant="primary"
                compact
                disabled={busy()}
                data-testid={`activity-proposal-keep-${w.handle}`}
                onClick={() =>
                  void runWatchAction(key, (c) =>
                    keepSessionWatchAction(
                      c,
                      w.session_id,
                      w.handle,
                      replacedRev() !== undefined
                        ? { revision: replacedRev() }
                        : {},
                    ),
                  )
                }
              >
                Keep
              </DenButton>
              <DenButton
                variant="secondary"
                compact
                disabled={busy()}
                data-testid={`activity-proposal-edit-${w.handle}`}
                onClick={() => props.onPrefillFromAgentWatch(w)}
              >
                Edit and keep
              </DenButton>
              <DenButton
                variant="ghost"
                compact
                disabled={busy()}
                data-testid={`activity-proposal-dismiss-${w.handle}`}
                onClick={() =>
                  void runWatchAction(key, (c) =>
                    cancelSessionWatchAction(c, w.session_id, w.handle),
                  )
                }
              >
                Dismiss
              </DenButton>
            </Show>
          </div>
        </div>

        <Show when={errText()}>
          {(msg) => (
            <p
              role="alert"
              class="text-den-caption text-den-err"
              data-testid={`activity-watch-error-${w.handle}`}
            >
              {msg()}
            </p>
          )}
        </Show>
      </li>
    );
  };

  return (
    <Show when={hasAnyLive()}>
      <section
        class="flex flex-col gap-den-3"
        aria-label="Live"
        data-testid="activity-live-section"
      >
        <h2 class="text-den-body font-medium text-den-text-primary">Live</h2>

        <Show when={props.queuedRuns.length > 0}>
          <ul
            class="flex flex-col gap-den-2"
            data-testid="activity-queued-runs"
          >
            <For each={props.queuedRuns}>
              {(run) => (
                <li
                  class="flex flex-wrap items-center justify-between gap-den-2 rounded-den-md border border-den-line bg-den-bg-surface p-den-2-5"
                  data-testid={`activity-queued-run-${run.run_id}`}
                >
                  <button
                    type="button"
                    class="den-chat-content-link text-den-body font-medium"
                    onClick={() => props.onSelectStandingWatch(run.watch_id)}
                  >
                    Queued · {run.watch_name}
                  </button>
                  <span class="text-den-caption text-den-text-muted">
                    Scheduled {formatTimeLabel(run.scheduled_at)}
                  </span>
                </li>
              )}
            </For>
          </ul>
        </Show>

        <Show when={sessionIds().length > 0}>
          <div
            class="project-running-groups flex flex-col gap-den-4"
            data-testid="project-running-groups"
          >
            <For each={sessionIds()}>
              {(sessionId) => {
                const group = () => groupForSession(sessionId);
                const activeRun = () => props.activeRunBySession.get(sessionId);
                const processIds = createMemo(
                  () => group()?.processes.map((p) => p.process_id) ?? [],
                );
                const processById = (processId: string) =>
                  group()?.processes.find((p) => p.process_id === processId);
                const sessionWatches = createMemo(() =>
                  watchesForSession(sessionId),
                );
                const standaloneWatches = createMemo(() => {
                  const procSet = new Set(processIds());
                  return sessionWatches().filter(
                    (w) => !w.source_process_id || !procSet.has(w.source_process_id),
                  );
                });
                const titleText = () =>
                  sessionTitle(
                    group()?.sessionTitle ?? activeRun()?.watch.name,
                  );

                return (
                  <section
                    class="project-running-group flex flex-col gap-den-2 rounded-den-md border bg-den-bg-surface p-den-3"
                    classList={{
                      "border-den-accent":
                        props.highlightedSessionId === sessionId,
                      "border-den-line":
                        props.highlightedSessionId !== sessionId,
                    }}
                    data-testid={`project-running-group-${sessionId}`}
                    aria-label={titleText()}
                  >
                    <header class="project-running-group__header flex flex-wrap items-center justify-between gap-den-2">
                      <div class="flex flex-wrap items-center gap-den-2">
                        <h3
                          class="text-den-body font-medium text-den-text-primary"
                          data-testid={`project-running-group-title-${sessionId}`}
                        >
                          {titleText()}
                        </h3>
                        <Show when={activeRun()} keyed>
                          {(entry) => (
                            <button
                              type="button"
                              class="den-chat-content-link text-den-caption"
                              data-testid={`activity-live-run-watch-${sessionId}`}
                              onClick={() =>
                                props.onSelectStandingWatch(
                                  entry.watch.watch_id,
                                )
                              }
                            >
                              Standing watch · {entry.watch.name}
                            </button>
                          )}
                        </Show>
                        <Show when={group()?.sessionArchived}>
                          <span
                            class="rounded-den-sm border border-den-line px-den-1-5 py-den-0-5 text-den-caption text-den-text-muted"
                            data-testid={`project-running-group-archived-${sessionId}`}
                          >
                            Archived
                          </span>
                        </Show>
                      </div>
                      <Show when={processIds().length === 0}>
                        <DenButton
                          variant="ghost"
                          compact
                          onClick={() => props.onOpenSession(sessionId)}
                        >
                          Open chat
                        </DenButton>
                      </Show>
                    </header>

                    <ul class="project-running-group__list flex flex-col gap-den-2">
                      <For each={processIds()}>
                        {(processId) => {
                          const proc = () => processById(processId);
                          const derived = () =>
                            sessionWatches().filter(
                              (w) => w.source_process_id === processId,
                            );
                          return (
                            <>
                              <ShowLatest when={proc()}>
                                {(item) => (
                                  <li
                                    class="project-running-row flex flex-col gap-den-1-5 rounded-den-sm border border-den-line p-den-2-5"
                                    data-testid={`project-running-row-${processId}`}
                                  >
                                    <div class="flex items-start justify-between gap-den-3">
                                      <code
                                        class="project-running-row__command font-mono text-den-body text-den-text-primary break-all"
                                        data-testid={`project-running-command-${processId}`}
                                      >
                                        {item().command}
                                      </code>
                                      <div class="project-running-row__actions flex shrink-0 items-center gap-den-2">
                                        <DenButton
                                          variant="ghost"
                                          compact
                                          data-testid={`project-running-open-chat-${processId}`}
                                          onClick={() =>
                                            props.onOpenSession(
                                              item().session_id,
                                            )
                                          }
                                        >
                                          Open chat
                                        </DenButton>
                                        <DenButton
                                          variant="secondary"
                                          compact
                                          data-testid={`project-running-stop-${processId}`}
                                          aria-label={`Stop ${item().command}`}
                                          disabled={
                                            props.isStopping(processId) ||
                                            props.isDisconnected
                                          }
                                          onClick={() =>
                                            props.onStopProcess(item())
                                          }
                                        >
                                          {props.isStopping(processId)
                                            ? "Stopping…"
                                            : "Stop"}
                                        </DenButton>
                                      </div>
                                    </div>

                                    <div class="project-running-row__meta flex flex-wrap items-center gap-den-3 text-den-caption text-den-text-muted">
                                      <Show when={item().ports.length > 0}>
                                        <span
                                          data-testid={`project-running-ports-${processId}`}
                                        >
                                          {formatPortsLabel(item().ports)}
                                        </span>
                                      </Show>
                                      <Show when={item().working_dir}>
                                        <span
                                          class="font-mono"
                                          data-testid={`project-running-workdir-${processId}`}
                                        >
                                          {item().working_dir}
                                        </span>
                                      </Show>
                                      <span
                                        data-testid={`project-running-started-${processId}`}
                                        data-tip={viewerCalendarTimeFormat().fullDateTime(
                                          Date.parse(item().started_at),
                                        )}
                                      >
                                        Started{" "}
                                        {formatTimeLabel(item().started_at)}
                                      </span>
                                    </div>

                                    <Show when={props.stopError(processId)}>
                                      {(errText) => (
                                        <p
                                          role="alert"
                                          class="text-den-caption text-den-err"
                                          data-testid={`project-running-stop-error-${processId}`}
                                        >
                                          {errText()}
                                        </p>
                                      )}
                                    </Show>
                                  </li>
                                )}
                              </ShowLatest>
                              <For each={derived()}>
                                {(dw) => renderAgentWatchRow(dw, true)}
                              </For>
                            </>
                          );
                        }}
                      </For>

                      <For each={standaloneWatches()}>
                        {(sw) => renderAgentWatchRow(sw, false)}
                      </For>
                    </ul>
                  </section>
                );
              }}
            </For>
          </div>
        </Show>
      </section>
    </Show>
  );
}
