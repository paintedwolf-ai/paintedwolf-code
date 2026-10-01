import { contextAction, bindContextAction } from "./context-actions.ts";
import { Show, createEffect, createSignal, onCleanup, type JSX } from "solid-js";
import { useTranscriptEntry } from "../chat/transcript/presentation/transcript-entry.ts";
import { TurnLoadFootLine } from "./tool/TurnLoadFootLine.tsx";
import {
  taskActivityDisplay,
  taskAgentType,
  taskCardStatus,
  taskDescription,
  type TaskActivityContext,
} from "../chat/task/task-card-model.ts";
import type { CitationGrounding, WorkerTask } from "../api/types.ts";
import { resolveCitationGrounding } from "../chat/worker/worker-evidence-model.ts";
import { citationGroundingPresent } from "../chat/grounding/citation-grounding-model.ts";
import { CitationGroundingBadge } from "./citation/CitationGroundingBadge.tsx";
import type { TranscriptLayout } from "../chat/transcript/layout/transcript-layout.ts";
import type { ToolPartView } from "../chat/tool/tool-part-model.ts";
import { taskJobIdFromPart } from "../chat/task/task-result-model.ts";
import {
  workerCardProgress,
  type WorkerCardProgress,
} from "../chat/worker/worker-progress.ts";
import { WorkerBudgetChip } from "./worker/WorkerBudgetChip.tsx";
import { WorkerToolCallsChip } from "./worker/WorkerToolCallsChip.tsx";
import { ContextRing } from "./chatview/ContextRing.tsx";
import { ContextMenu, type ContextMenuAnchor, type ContextMenuItem } from "./ContextMenu.tsx";
import { copyTextToClipboard } from "../utils/clipboard.ts";
import { addToChat } from "../chat/composer/add-to-chat.ts";
import { useChatDestinationScope } from "../chat/composer/chat-destination-scope.tsx";
import type { ChatDestination } from "../chat/composer/shared-composer-document.ts";
import {
  basenameOfPath,
  relativeUnderRoot,
  longestMatchingRoot,
  resolveProjectFile,
  type ResolveProjectRoot,
} from "../api/project-path.ts";
import { prefersReducedMotion } from "../platform/interaction/reduced-motion.ts";
import { formatSentenceCase } from "../format/format-sentence-case.ts";

const MAX_WORKER_CARD_PATH_MENU = 5;

type ShellProps = {
  agent: () => string;
  status: () => string;
  body: () => string;
  activity: () => string;
  grounding: () => CitationGrounding | undefined;
  worker: () => WorkerTask | undefined;
  clickable?: boolean | (() => boolean);
  onOpen?: () => void;
  onOpenEvidence?: () => void;
  kind?: "task" | "worker-summary";
  toolCallId?: string;
  entryKey?: string;
  sessionId?: string;
  /** Job id for Copy — worker.id preferred over enqueue payload. */
  jobId?: () => string | undefined;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  /** Closing line under the activity, such as how the tool came to be offered. */
  foot?: JSX.Element;
};

function shellClickable(
  clickable: ShellProps["clickable"],
): boolean {
  return typeof clickable === "function" ? clickable() : Boolean(clickable);
}

/** Minimum interval between worker activity commits. */
export const TASK_CARD_ACTIVITY_COOLDOWN_MS = 1100;

const TASK_CARD_TEXT_FADE_FROM = 0.38;
const TASK_CARD_TEXT_FADE_MS = 520;
/** Matches `--den-ease` in global.css. */
const TASK_CARD_TEXT_FADE_EASE = "cubic-bezier(0.4, 0, 0.2, 1)";

/** Fades committed activity text. */
function runTaskCardTextFade(node: HTMLParagraphElement | undefined) {
  if (!node || prefersReducedMotion() || typeof node.animate !== "function") {
    return;
  }
  node.animate(
    [{ opacity: TASK_CARD_TEXT_FADE_FROM }, { opacity: 1 }],
    { duration: TASK_CARD_TEXT_FADE_MS, easing: TASK_CARD_TEXT_FADE_EASE },
  );
}

/** Commits the leading change and one trailing change per cooldown. */
function TaskCardFadingActivity(props: { class: string; text: () => string }) {
  let el: HTMLParagraphElement | undefined;
  const [displayed, setDisplayed] = createSignal(props.text());
  const [fadeTick, setFadeTick] = createSignal(0);
  let initial = true;
  let pending: string | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  // Set on the first committed change.
  let lastCommitAt: number | undefined;

  createEffect(() => {
    fadeTick();
    if (fadeTick() === 0) return;
    runTaskCardTextFade(el);
  });

  const commit = (next: string) => {
    pending = undefined;
    lastCommitAt = Date.now();
    if (next === displayed()) return;
    setDisplayed(next);
    setFadeTick((t) => t + 1);
  };

  const startCooldown = () => {
    if (timer !== undefined) return;
    timer = setTimeout(() => {
      timer = undefined;
      if (pending != null) {
        commit(pending);
        // Continue the cooldown after a trailing commit.
        startCooldown();
      }
    }, TASK_CARD_ACTIVITY_COOLDOWN_MS);
  };

  createEffect(() => {
    const incoming = props.text();
    if (initial) {
      // Commit the first update immediately.
      initial = false;
      return;
    }
    if (incoming === displayed()) {
      pending = undefined;
      return;
    }
    const cooledDown =
      lastCommitAt === undefined ||
      Date.now() - lastCommitAt >= TASK_CARD_ACTIVITY_COOLDOWN_MS;
    if (timer === undefined && cooledDown) {
      commit(incoming);
      startCooldown();
      return;
    }
    pending = incoming;
    startCooldown();
  });

  onCleanup(() => {
    if (timer !== undefined) clearTimeout(timer);
  });

  return (
    <p ref={el} class={props.class}>
      {displayed()}
    </p>
  );
}


function workerCardMenuItems(opts: {
  onOpen?: () => void;
  onOpenEvidence?: () => void;
  jobId?: string;
  grounding?: CitationGrounding;
  touchedPaths?: readonly string[];
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  /** Absent asks which chat receives the path. */
  chatDestination?: ChatDestination;
}): ContextMenuItem[] {
  const items: ContextMenuItem[] = [];
  const onOpen = opts.onOpen;
  if (onOpen) {
    items.push(contextAction("openWorker", {
      testId: "task-card-menu-open",
      onSelect: onOpen,
    }));
  }
  const jobId = opts.jobId?.trim();
  if (jobId) {
    items.push(contextAction("copyJobID", {
      testId: "task-card-menu-copy-job-id",
      onSelect: () => {
        void copyTextToClipboard(jobId);
      },
    }));
  }
  const onOpenEvidence = opts.onOpenEvidence;
  if (onOpenEvidence && citationGroundingPresent(opts.grounding)) {
    items.push(contextAction("openEvidence", {
      testId: "task-card-menu-open-evidence",
      onSelect: onOpenEvidence,
    }));
  }

  const projectId = opts.projectId?.trim() ?? "";
  const refs = opts.rootRefs ?? [];
  if (projectId && refs.length > 0 && opts.touchedPaths?.length) {
    let added = 0;
    for (const raw of opts.touchedPaths) {
      if (added >= MAX_WORKER_CARD_PATH_MENU) break;
      const path = raw.trim();
      if (!path) continue;
      const resolved = resolveProjectFile({ roots: refs }, path);
      if ("error" in resolved) continue;
      const containingRoot = longestMatchingRoot(resolved.absolutePath, refs);
      const rel = containingRoot
        ? relativeUnderRoot(resolved.absolutePath, containingRoot.path)
        : null;
      if (!containingRoot || rel == null) continue;
      const name = basenameOfPath(rel === "." ? containingRoot.path : rel);
      items.push(bindContextAction({
        label: `Add ${name} to chat`,
        testId: "task-card-menu-add-path",
        onSelect: () => {
          void addToChat(
            {
              kind: "path-file",
              projectId,
              rootId: containingRoot.id,
              path: rel,
              name,
            },
            { destination: opts.chatDestination },
          );
        },
      }));
      added += 1;
    }
  }

  return items;
}

export function TaskCardShell(props: ShellProps) {
  const chatDestination = useChatDestinationScope();
  const { bindTranscriptEntry } = useTranscriptEntry(() => {
    const entryKey = props.entryKey?.trim();
    return entryKey
      ? { sessionId: props.sessionId, entryKey }
      : undefined;
  });
  const isClickable = () => shellClickable(props.clickable);
  const [menu, setMenu] = createSignal<ContextMenuAnchor | null>(null);

  const onKeyDown = (e: KeyboardEvent) => {
    if (!isClickable() || !props.onOpen) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      props.onOpen();
    }
  };

  const running = () => props.status() === "running";
  const progress = () => workerCardProgress(props.worker());

  const progressAriaLabel = (value: WorkerCardProgress) => {
    if (!running()) {
      if (props.status() === "canceled") return "Worker canceled";
      return props.status() === "error" ? "Worker failed" : "Worker finished";
    }
    const rounds = `Tool round ${value.used} of ${value.max}`;
    return value.batch
      ? `${rounds}, tool ${value.batch.done} of ${value.batch.total}`
      : rounds;
  };

  const percent = (fraction: number) =>
    `${Math.min(Math.max(fraction, 0), 1) * 100}%`;

  const resolveJobId = () =>
    props.jobId?.()?.trim() || props.worker()?.id?.trim() || undefined;

  const menuItems = () =>
    workerCardMenuItems({
      onOpen: props.onOpen,
      onOpenEvidence: props.onOpenEvidence,
      jobId: resolveJobId(),
      grounding: props.grounding(),
      touchedPaths: props.worker()?.touched_paths,
      projectId: props.projectId,
      rootRefs: props.rootRefs,
      chatDestination: chatDestination(),
    });

  return (
    <>
      <article
        ref={bindTranscriptEntry}
        class="den-task-card"
        classList={{
          "den-task-card--clickable": isClickable(),
          "den-task-card--running": running(),
        }}
        data-testid="task-card"
        data-tool-call-id={props.toolCallId}
        data-task-status={props.status()}
        data-task-kind={props.kind ?? "task"}
        data-job-id={resolveJobId()}
        onClick={() => {
          if (isClickable() && props.onOpen) props.onOpen();
        }}
        onKeyDown={onKeyDown}
        onContextMenu={(e) => {
          const items = menuItems();
          if (items.length === 0) return;
          e.preventDefault();
          e.stopPropagation();
          setMenu({ x: e.clientX, y: e.clientY });
        }}
        tabIndex={isClickable() ? 0 : undefined}
        role={isClickable() ? "button" : undefined}
        aria-label={
          isClickable() && props.onOpen
            ? `Open worker ${props.agent()} transcript`
            : undefined
        }
      >
        <header class="den-task-card-header">
          <span class="den-task-card-title">Worker · {props.agent()}</span>
          <div class="den-task-card-tags">
            <CitationGroundingBadge
              grounding={props.grounding()}
              onOpenDetail={props.onOpenEvidence}
            />
            <span
              class={`den-task-card-status den-task-card-status--${props.status()}`}
              classList={{ "den-task-card-status--live": running() }}
            >
              {formatSentenceCase(props.status())}
            </span>
            <Show when={props.worker()} keyed>
              {(worker) => (
                <>
                  <ContextRing
                    prompt={worker.context_usage?.prompt_tokens}
                    window={worker.context_usage?.window}
                    compactionThreshold={worker.context_usage?.compaction_threshold}
                    compact
                    testId="worker-context-meter"
                  />
                  <WorkerBudgetChip worker={() => worker} />
                  <WorkerToolCallsChip worker={() => worker} />
                </>
              )}
            </Show>
          </div>
        </header>
        <Show when={progress()}>
          {(value) => (
            <div
              class="den-task-card-progress"
              data-testid="task-card-progress"
              aria-label={progressAriaLabel(value())}
            >
              <div class="den-task-card-progress-track">
                <div
                  class="den-task-card-progress-fill"
                  style={{ width: percent(value().ratio) }}
                />
              </div>
              {/* The round's tools are most of its wall-clock, and the budget bar
                  cannot show them: one round is a couple of pixels wide. This is
                  the mark that moves while a round is being worked. */}
              <Show when={value().batch} keyed>
                {(batch) => (
                  <div
                    class="den-task-card-progress-track--batch den-task-card-progress-track"
                    data-testid="task-card-progress-batch"
                    data-batch-done={batch.done}
                    data-batch-total={batch.total}
                  >
                    <div
                      class="den-task-card-progress-fill den-task-card-progress-fill--batch"
                      style={{ width: percent(batch.ratio) }}
                    />
                  </div>
                )}
              </Show>
            </div>
          )}
        </Show>
        <p class="den-task-card-body">{props.body()}</p>
        <TaskCardFadingActivity class="den-task-card-activity" text={props.activity} />
        {props.foot}
      </article>
      <Show when={menu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={menuItems()}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
    </>
  );
}

type TaskProps = {
  part: ToolPartView;
  layout: TranscriptLayout;
  sessionId?: string;
  projectId?: string;
  rootRefs?: readonly ResolveProjectRoot[];
  worker?: () => WorkerTask | undefined;
  activityContext?: () => TaskActivityContext | undefined;
  onOpenWorker?: () => void;
  onOpenWorkerEvidence?: () => void;
};

/** Coordinator task/delegate_dispatch — full worker card (no tool chicklet). */
export function TaskCard(props: TaskProps) {
  const worker = () => props.worker?.();
  const activityContext = () => props.activityContext?.();

  return (
    <TaskCardShell
      agent={() => taskAgentType(props.part, worker())}
      status={() => taskCardStatus(props.part, worker())}
      body={() => taskDescription(props.part)}
      activity={() => taskActivityDisplay(props.part, activityContext())}
      grounding={() => resolveCitationGrounding(props.part.workerSummary, worker())}
      worker={worker}
      clickable={Boolean(props.onOpenWorker)}
      onOpen={props.onOpenWorker}
      onOpenEvidence={props.onOpenWorkerEvidence}
      kind="task"
      toolCallId={props.part.toolCallId}
      entryKey={props.part.id}
      sessionId={props.sessionId}
      jobId={() => worker()?.id?.trim() || taskJobIdFromPart(props.part)}
      projectId={props.projectId}
      rootRefs={props.rootRefs}
      foot={<TurnLoadFootLine part={props.part} />}
    />
  );
}
