import { Show, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AgentWatch, ProjectBackgroundProcess } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import {
  activityFocusRequest,
  clearActivityFocusRequest,
} from "../../platform/navigation/open-running.ts";
import {
  ensureProjectBackgroundHydrated,
  projectBackgroundFor,
  stopProjectBackgroundCommand,
} from "../../project/project-background-store.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import {
  DEFAULT_DAILY_RUN_CAP,
  DEFAULT_MIN_GAP_MINUTES,
  DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
  formatActivitySpendHeader,
  viewerTimeZone,
} from "../../watches/watch-format.ts";
import {
  buildEditorSeedFromAgentWatch,
  type StandingWatchEditorSeed,
} from "../../watches/watch-schedule-preview.ts";
import {
  ensureProjectWatchesHydrated,
  ensureSessionWatchesHydrated,
  projectWatchesFor,
  sessionWatchesFor,
} from "../../watches/watches-store.ts";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { ActivityEndedSection } from "../watches/ActivityEndedSection.tsx";
import { ActivityLiveSection } from "../watches/ActivityLiveSection.tsx";
import {
  StandingWatchesSection,
  seedFromExistingStandingWatch,
} from "../watches/StandingWatchesSection.tsx";

type Props = {
  projectId: string;
  appStore: AppStore;
  client?: LycaonClient | null;
  back?: StageBack | null;
  onOpenSession: (sessionId: string) => void;
};

function createBlankEditorSeed(): StandingWatchEditorSeed {
  return {
    mode: "create",
    name: "",
    prompt: "",
    conditions: [{ kind: "timer", every_minutes: 60 }],
    gate: null,
    minGapMinutes: DEFAULT_MIN_GAP_MINUTES,
    targetKind: "new_chat",
    targetSessionId: "",
    workflowId: "default",
    modelId: "",
    runSpendLimitNanoUsd: DEFAULT_RUN_SPEND_LIMIT_NANO_USD,
    allowUnpriced: false,
    dailyRunCap: DEFAULT_DAILY_RUN_CAP,
    timeZone: viewerTimeZone(),
    changedFields: new Set(),
  };
}

/** Project Context → Activity — standing watches, live work, and recently ended items. */
export function ProjectRunningView(props: Props) {
  const [selectedWatchId, setSelectedWatchId] = createSignal<string | null>(null);
  const [editorSeed, setEditorSeed] =
    createSignal<StandingWatchEditorSeed | null>(null);
  const [highlightedSessionId, setHighlightedSessionId] = createSignal<
    string | null
  >(null);

  const resolveClient = (): LycaonClient | null =>
    props.client ?? getLycaonClient();

  createEffect(() => {
    const projectId = props.projectId;
    const client = resolveClient();
    ensureProjectBackgroundHydrated(client, projectId);
    ensureProjectWatchesHydrated(client, projectId);
  });

  const bgView = () => projectBackgroundFor(props.projectId);
  const watchesView = () => projectWatchesFor(props.projectId);

  // Consume in-app Activity navigation intents (Edit and keep, Review in Activity, notice links).
  createEffect(() => {
    const intent = activityFocusRequest();
    if (!intent) return;
    if (intent.projectId && intent.projectId !== props.projectId) return;

    if (intent.createNew) {
      setSelectedWatchId(null);
      setEditorSeed(createBlankEditorSeed());
      clearActivityFocusRequest(intent.serial);
      return;
    }
    if (intent.watchId) {
      const target = watchesView().standingById.get(intent.watchId);
      setSelectedWatchId(intent.watchId);
      if (target) setEditorSeed(seedFromExistingStandingWatch(target));
      clearActivityFocusRequest(intent.serial);
      return;
    }
    if (intent.sessionId) {
      setHighlightedSessionId(intent.sessionId);
      clearActivityFocusRequest(intent.serial);
      return;
    }
    if (intent.prefillFromWatch) {
      const { sessionId, handle } = intent.prefillFromWatch;
      ensureSessionWatchesHydrated(resolveClient(), sessionId);
      const found =
        sessionWatchesFor(sessionId).byHandle.get(handle) ??
        watchesView().liveAgentWatches.find(
          (w) => w.session_id === sessionId && w.handle === handle,
        );
      if (!found) return;
      const replaced = found.standing_draft?.replaces
        ? watchesView().standingById.get(found.standing_draft.replaces)
        : undefined;
      if (found.standing_draft?.replaces && !replaced && !watchesView().hydrated) {
        return;
      }
      const seed = buildEditorSeedFromAgentWatch(found, replaced, Date.now());
      setSelectedWatchId(seed.watchId ?? null);
      setEditorSeed(seed);
      clearActivityFocusRequest(intent.serial);
    }
  });

  const isDisconnected = () =>
    props.appStore.state.sidecarStatus === "disconnected" ||
    props.appStore.state.sidecarStatus === "reconnecting";

  const hasStandingSection = () =>
    watchesView().standingWatches.length > 0 || editorSeed() != null;
  const hasLiveSection = () =>
    bgView().runningGroups.length > 0 ||
    watchesView().liveAgentWatches.length > 0 ||
    watchesView().activeRunBySession.size > 0 ||
    watchesView().queuedRuns.length > 0;
  const hasEndedSection = () =>
    bgView().recentlyEnded.length > 0 ||
    watchesView().recentlyEndedRuns.length > 0;

  const hasCachedRows = () =>
    hasStandingSection() || hasLiveSection() || hasEndedSection();
  const isHydrated = () => bgView().hydrated || watchesView().hydrated;
  const showUnhydratedPlaceholder = () =>
    (isDisconnected() || !isHydrated()) && !hasCachedRows();
  const stageError = () => bgView().error ?? watchesView().error;

  const spendLine = createMemo(() =>
    formatActivitySpendHeader(
      watchesView().standingWatches,
      watchesView().hasUnpricedStandingWatch,
    ),
  );

  const openCreateEditor = () => {
    setSelectedWatchId(null);
    setEditorSeed(createBlankEditorSeed());
  };

  const openPrefillFromWatch = (watch: AgentWatch) => {
    const replaced = watch.standing_draft?.replaces
      ? watchesView().standingById.get(watch.standing_draft.replaces)
      : undefined;
    const seed = buildEditorSeedFromAgentWatch(watch, replaced, Date.now());
    setSelectedWatchId(seed.watchId ?? null);
    setEditorSeed(seed);
  };

  const handleStopProcess = (proc: ProjectBackgroundProcess) => {
    const client = resolveClient();
    if (!client) return;
    void stopProjectBackgroundCommand(
      client,
      props.projectId,
      proc.process_id,
    );
  };

  const allEmpty = () =>
    !hasStandingSection() && !hasLiveSection() && !hasEndedSection();

  return (
    <BrowseStagePanel
      appStore={props.appStore}
      back={props.back}
      title="Activity"
      scrollHost="main"
      requireClient={false}
      testId="project-running-view"
      error={!showUnhydratedPlaceholder() ? (stageError() ?? undefined) : undefined}
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
                : "Loading activity…"}
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

          <Show when={!allEmpty()}>
            <header
              class="flex flex-wrap items-center justify-between gap-den-2"
              data-testid="activity-header"
            >
              <div class="text-den-caption text-den-text-muted">
                <Show when={spendLine()}>
                  {(line) => (
                    <span data-testid="activity-daily-spend-line">
                      {line()}
                    </span>
                  )}
                </Show>
              </div>
              <DenButton
                variant="primary"
                compact
                disabled={isDisconnected()}
                data-testid="activity-new-standing-watch"
                onClick={openCreateEditor}
              >
                New standing watch
              </DenButton>
            </header>
          </Show>

          <Show
            when={!allEmpty()}
            fallback={
              <Show when={isHydrated() && !isDisconnected() && !stageError()}>
                <div
                  class="flex flex-col items-start gap-den-2-5"
                  data-testid="activity-empty-state"
                >
                  <p
                    class="den-browse-stage__hint"
                    data-testid="project-running-empty"
                  >
                    Nothing running or watching
                  </p>
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="activity-empty-new-standing-watch"
                    onClick={openCreateEditor}
                  >
                    New standing watch
                  </DenButton>
                </div>
              </Show>
            }
          >
            <StandingWatchesSection
              projectId={props.projectId}
              standingWatches={watchesView().standingWatches}
              selectedWatchId={selectedWatchId()}
              editorSeed={editorSeed()}
              client={resolveClient}
              isDisconnected={isDisconnected()}
              onSelectWatch={setSelectedWatchId}
              onOpenEditorSeed={setEditorSeed}
              onOpenSession={props.onOpenSession}
            />

            <ActivityLiveSection
              runningGroups={bgView().runningGroups}
              liveAgentWatches={watchesView().liveAgentWatches}
              activeRunBySession={watchesView().activeRunBySession}
              standingById={watchesView().standingById}
              queuedRuns={watchesView().queuedRuns}
              highlightedSessionId={highlightedSessionId()}
              isDisconnected={isDisconnected()}
              isStopping={(id) => bgView().stoppingIds.has(id)}
              stopError={(id) => bgView().stopErrors.get(id) ?? null}
              client={resolveClient}
              onStopProcess={handleStopProcess}
              onOpenSession={props.onOpenSession}
              onSelectStandingWatch={(watchId) => {
                const sw = watchesView().standingById.get(watchId);
                setSelectedWatchId(watchId);
                if (sw) setEditorSeed(seedFromExistingStandingWatch(sw));
              }}
              onPrefillFromAgentWatch={openPrefillFromWatch}
            />

            <ActivityEndedSection
              endedProcesses={bgView().recentlyEnded}
              endedRuns={watchesView().recentlyEndedRuns}
              onOpenSession={props.onOpenSession}
            />
          </Show>
        </Show>
      </div>
    </BrowseStagePanel>
  );
}
