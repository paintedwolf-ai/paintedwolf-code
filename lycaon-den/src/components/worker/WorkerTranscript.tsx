import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, untrack, type Accessor } from "solid-js";
import type { FindingsDigest, Message, WorkerSummaryMeta, WorkerTask } from "../../api/types.ts";
import { citationGroundingPresent } from "../../chat/grounding/citation-grounding-model.ts";
import {
  buildWorkerEvidenceFallbackView,
  resolveCitationGrounding,
  workerEvidenceSectionId,
  workerSummaryMetaForJob,
} from "../../chat/worker/worker-evidence-model.ts";
import {
  workerDispatchParamRows,
  workerDispatchBrief,
  workerRowStatus,
  type WorkerDrawerFocus,
} from "../../chat/worker/workers-model.ts";
import { liveGeneratingTokensFromMessages } from "../../chat/transcript/projection/generating-tokens.ts";
import { workerTranscriptRowsForDisplay } from "../../chat/worker/worker-transcript-coalesce.ts";
import { loadWorkerTranscriptPage, watchWorkerTranscriptLoadMore } from "../../chat/worker/worker-transcript-load-more.ts";
import type { TranscriptPageEdge } from "../../chat/transcript/layout/transcript-window.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { scrollportMotionContaining } from "../../platform/scrolling/scrollport-motion.ts";
import { scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { observeSharedContentBox } from "../../layout/shared-resize-observer.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { valueOf } from "../../store/load-state.ts";
import { ChatDestinationScope, chatDestinationOf } from "../../chat/composer/chat-destination-scope.tsx";
import { bindFindableView } from "../../find/use-findable-view.ts";
import { MarkdownBody } from "../transcript/MarkdownBody.tsx";
import { SessionTranscript } from "../transcript/SessionTranscript.tsx";
import { WorkerCoordinationSection } from "./WorkerCoordinationSection.tsx";
import { WorkerEvidenceSection } from "./WorkerEvidenceSection.tsx";
import {
  WorkerTranscriptSection,
  type WorkerSectionKey,
} from "./WorkerTranscriptSection.tsx";
import { WorkerWorkingIndicator } from "./WorkerWorkingIndicator.tsx";
import {
  pinDrawerTranscriptSection,
} from "../../shell/drawer-transcript-scroll.ts";

type Props = {
  workerId: string | null;
  workers: readonly WorkerTask[];
  appStore: AppStore;
  projectDir: string;
  emptyLabel?: string;
  scrollToFocus?: WorkerDrawerFocus;
  onScrollToFocusHandled?: () => void;
};

type WorkerTranscriptPaneProps = {
  worker: Accessor<WorkerTask>;
  appStore: AppStore;
  projectDir: string;
  setPaneEl: (el: HTMLDivElement) => void;
  dispatchPrompt: Accessor<string>;
  dispatchParams: Accessor<ReturnType<typeof workerDispatchParamRows>>;
  displayMessages: Accessor<Message[]>;
  isWorking: Accessor<boolean>;
  generatingTokens: Accessor<number>;
  findingsDigest: Accessor<FindingsDigest | undefined>;
  summaryMeta: Accessor<WorkerSummaryMeta | undefined>;
  evidenceHighlight: Accessor<boolean>;
  activityPreview: Accessor<string | null>;
  selectSection: (key: WorkerSectionKey) => void;
};

function WorkerTranscriptPane(props: WorkerTranscriptPaneProps) {
  const workerId = untrack(() => props.worker().id);
  onCleanup(props.appStore.actions.retainWorkerTranscript(workerId));
  const [paneRoot, setPaneRoot] = createSignal<HTMLDivElement | null>(null);
  const [activityBody, setActivityBody] = createSignal<HTMLElement>();
  const [loadingEdge, setLoadingEdge] = createSignal<TranscriptPageEdge>();
  bindFindableView({
    id: "worker-transcript",
    root: paneRoot,
  });
  const resident = () => props.appStore.state.workerTranscripts[workerId]?.window;
  // Rows wait for the first page when the reader meets the activity from above.
  const activityRows = () => (loadingEdge() === "start" ? [] : props.displayMessages());
  // Across a tail gap the rows end before the live tail, so nothing below them is current.
  const showsLiveEnd = () => !resident()?.hasTailGap;
  createEffect(() => {
    const body = activityBody();
    const pane = paneRoot();
    const viewport = pane ? scrollportMotionContaining(pane)?.viewport : undefined;
    if (!body || !pane || !viewport) return;
    const watcher = watchWorkerTranscriptLoadMore({
      viewport,
      activity: activityBody,
      resident,
      load: (edge) => {
        const client = getLycaonClient();
        return client
          ? loadWorkerTranscriptPage(client, props.appStore, untrack(props.worker), edge)
          : Promise.resolve(false);
      },
      onLoading: setLoadingEdge,
    });
    onCleanup(watcher.stop);
    // The last section sizes itself to the reading area so it can scroll into its slot.
    onCleanup(observeSharedContentBox(viewport, () => {
      pane.style.setProperty("--worker-reading-h", `${viewport.clientHeight}px`);
    }));
    // New rows can bring an edge into reach without a scroll.
    createEffect(on(resident, () => scheduleScrollportFrame(viewport, "observe", watcher.check)));
  });
  return (
    <div
      class="den-worker-transcript-pane"
      ref={(el) => {
        setPaneRoot(el);
        props.setPaneEl(el);
      }}
    >
      <Show when={props.dispatchPrompt()}>
        <WorkerTranscriptSection
          title="Task"
          sectionKey="task"
          class="den-worker-transcript-task"
          data-testid="worker-dispatch"
          status="complete"
          onSelect={() => props.selectSection("task")}
        >
          <MarkdownBody
            source={props.dispatchPrompt()}
            literalHtml
            class="assistant-prose"
            projectId={props.worker().project_id?.trim() || undefined}
          />
          <Show when={props.dispatchParams().length > 0}>
            <dl class="den-worker-transcript-params">
              <For each={props.dispatchParams()}>
                {(param) => (
                  <>
                    <dt>{param.label}</dt>
                    <dd>{param.value}</dd>
                  </>
                )}
              </For>
            </dl>
          </Show>
        </WorkerTranscriptSection>
      </Show>
      <WorkerTranscriptSection
        title="Activity"
        sectionKey="activity"
        class="den-worker-transcript-activity"
        data-testid="worker-activity"
        status={props.isWorking() ? "working" : "complete"}
        onSelect={() => props.selectSection("activity")}
        bodyRef={setActivityBody}
        busy={loadingEdge() !== undefined}
      >
        <Show when={activityRows().length > 0}>
          <SessionTranscript
            messages={activityRows()}
            layout="worker"
            sessionId={props.worker().child_session_id?.trim() || null}
            projectId={props.worker().project_id?.trim() || undefined}
            branchJobId={props.worker().id}
            checkpointClient={getLycaonClient()}
            checkpointAppStore={props.appStore}
            projectDir={props.projectDir}
          />
        </Show>
        <Show
          when={
            props.displayMessages().length === 0 &&
            !props.isWorking() &&
            !props.activityPreview()
          }
        >
          <p class="den-worker-transcript-empty den-worker-transcript-empty--inline">
            No activity yet for this worker.
          </p>
        </Show>
        <Show when={activityRows().length === 0 && props.activityPreview()}>
          {(line) => (
            <p
              class="den-worker-transcript-activity-preview"
              data-testid="worker-activity-preview"
            >
              {line()}
            </p>
          )}
        </Show>
        <Show when={props.isWorking() && showsLiveEnd()}>
          <WorkerWorkingIndicator generatingTokens={props.generatingTokens()} />
        </Show>
      </WorkerTranscriptSection>
      <WorkerCoordinationSection
        worker={props.worker()}
        findings={props.findingsDigest()}
        roster={props.appStore.state.board?.roster}
        onSelect={() => props.selectSection("coordination")}
      />
      <WorkerEvidenceSection
        worker={props.worker()}
        summaryMeta={props.summaryMeta()}
        highlight={props.evidenceHighlight()}
        onSelect={() => props.selectSection("evidence")}
      />
    </div>
  );
}

/** Renders cached rows; the drawer hydrates them. */
export function WorkerTranscript(props: Props) {
  const [evidenceHighlight, setEvidenceHighlight] = createSignal(false);
  let paneEl: HTMLDivElement | undefined;

  const worker = createMemo(() => {
    const id = props.workerId?.trim();
    if (!id) return null;
    return props.workers.find((row) => row.id === id) ?? null;
  });

  const summaryMeta = createMemo(() => {
    const row = worker();
    if (!row) return undefined;
    return workerSummaryMetaForJob(props.appStore.state.messages, row.id);
  });

  const displayMessages = createMemo(() => {
    const row = worker();
    if (!row) return [];
    const cached = workerTranscriptRowsForDisplay(props.appStore, row.id);
    return cached ? [...cached] : [];
  });

  const activityPreview = createMemo(() => {
    const row = worker();
    if (!row) return null;
    const used = row.tool_loops_used ?? 0;
    if (used <= 0) return null;
    const max = row.max_tool_loops;
    return max ? `${used} of ${max} tool turns so far` : `${used} tool turns so far`;
  });

  const isWorking = createMemo(() => {
    const row = worker();
    if (!row) return false;
    return workerRowStatus(row) === "running";
  });

  const generatingTokens = createMemo(() =>
    liveGeneratingTokensFromMessages(displayMessages()),
  );

  const dispatchPrompt = createMemo(() => {
    const row = worker();
    if (!row) return "";
    return workerDispatchBrief(row);
  });

  const dispatchParams = createMemo(() => {
    const row = worker();
    if (!row) return [];
    return workerDispatchParamRows(row, props.appStore.state.messages);
  });

  const findingsDigest = createMemo(
    () => valueOf(props.appStore.state.findings) ?? undefined,
  );

  const hasEvidenceSection = createMemo(() => {
    const row = worker();
    if (!row) return false;
    const meta = summaryMeta();
    const grounding = resolveCitationGrounding(meta, row);
    if (grounding && citationGroundingPresent(grounding)) return true;
    return Boolean(buildWorkerEvidenceFallbackView(meta, row, row.id));
  });

  const sectionOrder = createMemo((): WorkerSectionKey[] => {
    const keys: WorkerSectionKey[] = [];
    if (dispatchPrompt()) keys.push("task");
    keys.push("activity");
    keys.push("coordination");
    if (hasEvidenceSection()) keys.push("evidence");
    return keys;
  });

  const sectionIndex = (key: WorkerSectionKey) => sectionOrder().indexOf(key);

  // Section bodies align below the sticky headers.
  const selectSection = (key: WorkerSectionKey) => {
    void pinDrawerTranscriptSection({
      pane: paneEl,
      sectionKey: key,
      sectionIndex: sectionIndex(key),
    });
  };

  createEffect(() => {
    const id = props.workerId?.trim();
    if (!id || props.scrollToFocus !== "evidence") return;

    const scrollToEvidence = (attemptsLeft: number) => {
      const el = document.getElementById(workerEvidenceSectionId(id));
      if (el) {
        scrollportMotionContaining(el)?.revealElement(el, {
          glide: true,
          block: "start",
        });
        setEvidenceHighlight(true);
        window.setTimeout(() => setEvidenceHighlight(false), 1200);
        props.onScrollToFocusHandled?.();
        return;
      }
      if (attemptsLeft > 0) {
        requestAnimationFrame(() => scrollToEvidence(attemptsLeft - 1));
        return;
      }
      props.onScrollToFocusHandled?.();
    };
    queueMicrotask(() => scrollToEvidence(8));
  });

  createEffect(
    on(
      () => props.workerId?.trim(),
      (id, prevId) => {
        if (!id || id === prevId || props.scrollToFocus === "evidence") return;
        queueMicrotask(() => {
          if (!paneEl) return;
          const motion = scrollportMotionContaining(paneEl);
          motion?.cancelApplicationMotion();
          motion?.commit(0, "jump");
        });
      },
    ),
  );

  return (
    <Show
      when={props.workerId?.trim()}
      keyed
      fallback={
        <p class="den-worker-transcript-empty">
          {props.emptyLabel ?? "Select a worker to view activity."}
        </p>
      }
    >
      {(_workerId) => (
        <Show
          when={worker()}
          fallback={
            <p class="den-worker-transcript-empty">
              {props.emptyLabel ?? "Select a worker to view activity."}
            </p>
          }
        >
          <ChatDestinationScope
            destination={chatDestinationOf(worker()?.project_id, worker()?.parent_session_id)}
          >
            <WorkerTranscriptPane
              worker={worker as Accessor<WorkerTask>}
              appStore={props.appStore}
              projectDir={props.projectDir}
              setPaneEl={(el) => {
                paneEl = el;
              }}
              dispatchPrompt={dispatchPrompt}
              dispatchParams={dispatchParams}
              displayMessages={displayMessages}
              isWorking={isWorking}
              generatingTokens={generatingTokens}
              activityPreview={activityPreview}
              findingsDigest={findingsDigest}
              summaryMeta={summaryMeta}
              evidenceHighlight={evidenceHighlight}
              selectSection={selectSection}
            />
          </ChatDestinationScope>
        </Show>
      )}
    </Show>
  );
}
