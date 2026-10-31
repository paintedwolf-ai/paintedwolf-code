import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ProjectBackgroundProcess } from "../../api/types.ts";
import { sessionTitle } from "../../chat/session/session-title.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { formatBackgroundTerminationReason } from "../../project/background-termination-reason.ts";
import {
  ensureProjectBackgroundHydrated,
  projectBackgroundFor,
  stopProjectBackgroundCommand,
} from "../../project/project-background-store.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";

type Props = {
  projectId: string;
  appStore: AppStore;
  client?: LycaonClient | null;
  back?: StageBack | null;
  onOpenSession: (sessionId: string) => void;
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

/** Project Context → Running — live background commands grouped by chat, plus recently ended commands. */
export function ProjectRunningView(props: Props) {
  const [endedExpanded, setEndedExpanded] = createSignal(false);

  const resolveClient = (): LycaonClient | null =>
    props.client ?? getLycaonClient();

  createEffect(() => {
    const projectId = props.projectId;
    const client = resolveClient();
    ensureProjectBackgroundHydrated(client, projectId);
  });

  const view = () => projectBackgroundFor(props.projectId);
  const isDisconnected = () =>
    props.appStore.state.sidecarStatus === "disconnected" ||
    props.appStore.state.sidecarStatus === "reconnecting";
  const hasCachedRows = () =>
    view().runningGroups.length > 0 || view().recentlyEnded.length > 0;
  const showUnhydratedPlaceholder = () =>
    (isDisconnected() || !view().hydrated) && !hasCachedRows();

  const runningSessionIds = createMemo(() =>
    view().runningGroups.map((g) => g.sessionId),
  );
  const groupForSession = (sessionId: string) =>
    view().runningGroups.find((g) => g.sessionId === sessionId);
  const endedProcessIds = createMemo(() =>
    view().recentlyEnded.map((p) => p.process_id),
  );
  const endedProcessById = (processId: string) =>
    view().recentlyEnded.find((p) => p.process_id === processId);

  const isStopping = (processId: string) => view().stoppingIds.has(processId);
  const stopError = (processId: string) =>
    view().stopErrors.get(processId) ?? null;

  const handleStop = (proc: ProjectBackgroundProcess) => {
    const client = resolveClient();
    if (!client) return;
    void stopProjectBackgroundCommand(
      client,
      props.projectId,
      proc.process_id,
    );
  };

  return (
    <BrowseStagePanel
      appStore={props.appStore}
      back={props.back}
      title="Running"
      scrollHost="main"
      requireClient={false}
      testId="project-running-view"
      error={
        !showUnhydratedPlaceholder() ? (view().error ?? undefined) : undefined
      }
      errorTestId="project-running-error"
    >
      <div class="project-running-view flex flex-col gap-den-4 px-den-stage-x py-den-3">
        <Show
          when={!showUnhydratedPlaceholder()}
          fallback={
            <p
              class="den-browse-stage__hint"
              data-testid="project-running-unhydrated"
            >
              {isDisconnected()
                ? "Reconnecting to backend…"
                : "Loading running commands…"}
            </p>
          }
        >
          <Show when={isDisconnected() && hasCachedRows()}>
            <p
              class="den-browse-stage__hint"
              data-testid="project-running-reconnecting"
            >
              Reconnecting to backend…
            </p>
          </Show>

          <Show
            when={runningSessionIds().length > 0}
            fallback={
              <Show when={view().hydrated && !isDisconnected() && !view().error}>
                <p
                  class="den-browse-stage__hint"
                  data-testid="project-running-empty"
                >
                  No background commands are running. Servers and watchers an
                  agent starts appear here until they are stopped.
                </p>
              </Show>
            }
          >
            <div
              class="project-running-groups flex flex-col gap-den-4"
              data-testid="project-running-groups"
            >
              <For each={runningSessionIds()}>
                {(sessionId) => {
                  const group = () => groupForSession(sessionId);
                  const processIds = createMemo(
                    () => group()?.processes.map((p) => p.process_id) ?? [],
                  );
                  const processById = (processId: string) =>
                    group()?.processes.find((p) => p.process_id === processId);

                  return (
                    <section
                      class="project-running-group flex flex-col gap-den-2 rounded-den-md border border-den-line bg-den-bg-surface p-den-3"
                      data-testid={`project-running-group-${sessionId}`}
                      aria-label={sessionTitle(group()?.sessionTitle)}
                    >
                      <header class="project-running-group__header flex items-center justify-between gap-den-2">
                        <div class="flex items-center gap-den-2">
                          <h3
                            class="text-den-body font-medium text-den-text-primary"
                            data-testid={`project-running-group-title-${sessionId}`}
                          >
                            {sessionTitle(group()?.sessionTitle)}
                          </h3>
                          <Show when={group()?.sessionArchived}>
                            <span
                              class="rounded-den-sm border border-den-line px-den-1-5 py-den-0-5 text-den-caption text-den-text-muted"
                              data-testid={`project-running-group-archived-${sessionId}`}
                            >
                              Archived
                            </span>
                          </Show>
                        </div>
                      </header>

                      <ul class="project-running-group__list flex flex-col gap-den-2">
                        <For each={processIds()}>
                          {(processId) => {
                            const proc = () => processById(processId);
                            return (
                              <Show when={proc()}>
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
                                            isStopping(processId) ||
                                            isDisconnected()
                                          }
                                          onClick={() => handleStop(item())}
                                        >
                                          {isStopping(processId)
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
                                        data-tip={item().started_at}
                                      >
                                        Started{" "}
                                        {formatTimeLabel(item().started_at)}
                                      </span>
                                    </div>

                                    <Show when={stopError(processId)}>
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
                              </Show>
                            );
                          }}
                        </For>
                      </ul>
                    </section>
                  );
                }}
              </For>
            </div>
          </Show>

          <Show when={endedProcessIds().length > 0}>
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
                <span>Recently ended ({endedProcessIds().length})</span>
              </button>

              <Show when={endedExpanded()}>
                <ul
                  class="project-running-ended__list flex flex-col gap-den-2"
                  data-testid="project-running-ended-list"
                >
                  <For each={endedProcessIds()}>
                    {(processId) => {
                      const proc = () => endedProcessById(processId);
                      return (
                        <Show when={proc()}>
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
                                  <span class="font-mono">
                                    {item().working_dir}
                                  </span>
                                </Show>
                                <Show when={item().ended_at}>
                                  {(endedAt) => (
                                    <span data-tip={endedAt()}>
                                      Ended {formatTimeLabel(endedAt())}
                                    </span>
                                  )}
                                </Show>
                              </div>
                            </li>
                          )}
                        </Show>
                      );
                    }}
                  </For>
                </ul>
              </Show>
            </section>
          </Show>
        </Show>
      </div>
    </BrowseStagePanel>
  );
}
