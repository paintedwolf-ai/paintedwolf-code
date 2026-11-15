import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  type Accessor,
} from "solid-js";
import type {
  BoardView,
  WorkerHistoryMessage,
  WorkerHistoryRound,
  WorkerTask,
} from "../../api/types.ts";
import {
  WORKER_HISTORY_PAGE_LIMIT,
  mergeWorkerHistoryMessages,
  mergeWorkerHistoryRounds,
  resolveHistoryFocusElement,
  workerHistorySectionId,
  workerHistorySenderLabel,
  workerInstructionStatusLabel,
  workerRoundDisposition,
  type WorkerHistoryFocusTarget,
} from "../../chat/worker/worker-history-model.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { scrollportMotionContaining } from "../../platform/scrolling/scrollport-motion.ts";
import { boardWorkerForTask } from "../../store/workers-board-sync.ts";
import { WorkerSummarySection } from "./WorkerSummarySection.tsx";
import { WorkerTranscriptSection } from "./WorkerTranscriptSection.tsx";

export { workerHistorySectionId, type WorkerHistoryFocusTarget };

export function WorkerHistorySection(props: {
  worker: Accessor<WorkerTask>;
  board?: Accessor<BoardView | undefined>;
  focusTarget?: Accessor<WorkerHistoryFocusTarget | null>;
  onFocusHandled?: () => void;
  onSelect: () => void;
}) {
  const [rounds, setRounds] = createSignal<WorkerHistoryRound[]>([]);
  const [messages, setMessages] = createSignal<WorkerHistoryMessage[]>([]);
  const [before, setBefore] = createSignal<number | undefined>();
  const [beforeRound, setBeforeRound] = createSignal<number | undefined>();
  const [loading, setLoading] = createSignal(false);
  const [loadingEarlier, setLoadingEarlier] = createSignal(false);
  const [expandedSummaries, setExpandedSummaries] = createSignal<Record<number, boolean>>({});
  const [highlightedKey, setHighlightedKey] = createSignal<string | null>(null);
  let sectionBodyEl: HTMLElement | undefined;

  const projectId = () => props.worker().project_id?.trim() || "";
  const sessionId = () => props.worker().parent_session_id?.trim() || "";
  const workerTarget = () =>
    props.worker().worker_record_id?.trim() ||
    props.worker().worker_name?.trim() ||
    props.worker().id.trim();
  const unreadCount = () =>
    boardWorkerForTask(props.board?.(), props.worker())?.unread_message_count ?? 0;

  const refreshKey = createMemo(() => {
    const row = props.worker();
    return [
      projectId(),
      sessionId(),
      workerTarget(),
      row.id,
      row.round ?? 1,
      row.status,
      row.result_disposition ?? "",
      unreadCount(),
    ].join("|");
  });

  let previousTarget = "";
  createEffect(() => {
    refreshKey();
    const pid = projectId();
    const sid = sessionId();
    const target = workerTarget();
    const client = getLycaonClient();
    if (!pid || !sid || !target || !client) return;
    const switchedWorker = `${pid}|${sid}|${target}` !== previousTarget;
    previousTarget = `${pid}|${sid}|${target}`;
    if (switchedWorker) {
      setRounds([]);
      setMessages([]);
      setBefore(undefined);
      setBeforeRound(undefined);
      setExpandedSummaries({});
    }
    const controller = new AbortController();
    onCleanup(() => controller.abort());
    setLoading(true);
    void client
      .getWorkerHistory(
        pid,
        { sessionId: sid, worker: target, limit: WORKER_HISTORY_PAGE_LIMIT },
        { signal: controller.signal },
      )
      .then((page) => {
        if (controller.signal.aborted) return;
        setRounds((prev) => mergeWorkerHistoryRounds(switchedWorker ? [] : prev, page.rounds ?? []));
        setMessages((prev) =>
          mergeWorkerHistoryMessages(switchedWorker ? [] : prev, page.messages ?? []),
        );
        if (switchedWorker) {
          setBefore(page.before);
          setBeforeRound(page.before_round);
        } else {
          setBefore((prev) => prev ?? page.before);
          setBeforeRound((prev) => prev ?? page.before_round);
        }
      })
      .catch(() => {
        // Keep previously loaded rows visible when a background refresh fails.
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
  });

  const hasMoreEarlier = () => (before() ?? 0) > 0 || (beforeRound() ?? 0) > 0;

  const loadEarlier = async () => {
    const pid = projectId();
    const sid = sessionId();
    const target = workerTarget();
    const client = getLycaonClient();
    if (!pid || !sid || !target || !client || loadingEarlier() || !hasMoreEarlier()) return;
    setLoadingEarlier(true);
    try {
      const page = await client.getWorkerHistory(pid, {
        sessionId: sid,
        worker: target,
        before: (before() ?? 0) > 0 ? before() : undefined,
        beforeRound: (beforeRound() ?? 0) > 0 ? beforeRound() : undefined,
        limit: WORKER_HISTORY_PAGE_LIMIT,
      });
      setRounds((prev) => mergeWorkerHistoryRounds(prev, page.rounds ?? []));
      setMessages((prev) => mergeWorkerHistoryMessages(prev, page.messages ?? []));
      setBefore(page.before);
      setBeforeRound(page.before_round);
    } finally {
      setLoadingEarlier(false);
    }
  };

  const toggleSummary = (ordinal: number) =>
    setExpandedSummaries((prev) => ({ ...prev, [ordinal]: !prev[ordinal] }));

  createEffect(() => {
    const target = props.focusTarget?.();
    if (!target || loading()) return;
    queueMicrotask(() => {
      const { element: matchEl, key } = resolveHistoryFocusElement(sectionBodyEl, target);
      const revealEl = matchEl ?? document.getElementById(workerHistorySectionId(props.worker().id));
      if (revealEl) {
        const motion = scrollportMotionContaining(revealEl);
        if (motion) motion.revealElement(revealEl, { glide: true, block: "start" });
        else if (typeof revealEl.scrollIntoView === "function") revealEl.scrollIntoView({ block: "nearest" });
      }
      if (key) {
        setHighlightedKey(key);
        window.setTimeout(() => setHighlightedKey(null), 1400);
      }
      props.onFocusHandled?.();
    });
  });

  const isEmpty = () => rounds().length === 0 && messages().length === 0 && !loading();
  const visible = () => loading() || !isEmpty() || Boolean(props.focusTarget?.());

  return (
    <Show when={visible()}>
      <WorkerTranscriptSection
        title="History"
        sectionKey="history"
        id={workerHistorySectionId(props.worker().id)}
        class="den-worker-transcript-history"
        data-testid="worker-history-section"
        status={loading() ? "working" : "complete"}
        busy={loading() || loadingEarlier()}
        bodyRef={(el) => {
          sectionBodyEl = el;
        }}
        onSelect={props.onSelect}
      >
      <Show when={hasMoreEarlier()}>
        <button
          type="button"
          class="den-worker-history-action"
          data-testid="worker-history-load-more"
          disabled={loadingEarlier()}
          onClick={() => void loadEarlier()}
        >
          {loadingEarlier() ? "Loading earlier…" : "Load earlier"}
        </button>
      </Show>
      <Show
        when={!isEmpty()}
        fallback={<p class="den-worker-transcript-empty den-worker-transcript-empty--inline">No rounds or messages recorded yet.</p>}
      >
        <Show when={messages().length > 0}>
          <section class="den-worker-history-group">
            <h4 class="den-worker-transcript-coordination-heading">Messages and instructions</h4>
            <ul class="den-worker-history-list">
              <For each={messages()}>
                {(msg) => (
                  <li
                    class="den-worker-history-item"
                    classList={{ "den-worker-history-item--highlight": highlightedKey() === `msg:${msg.seq}` }}
                    data-testid="worker-history-message"
                    data-message-seq={msg.seq}
                    data-sender-role={msg.sender_role}
                    data-instruction-status={msg.instruction_status}
                    data-needs-reply={msg.needs_reply ? "true" : undefined}
                  >
                    <div class="den-worker-history-meta">
                      <span class="den-worker-history-sender">{workerHistorySenderLabel(msg.sender_role)}</span>
                      <Show when={(msg.round_ordinal ?? 0) > 0}>
                        <span class="den-worker-fact-badge">Round {msg.round_ordinal}</span>
                      </Show>
                      <Show when={msg.instruction_status} keyed>
                        {(status) => (
                          <span class="den-worker-fact-badge" data-testid="worker-instruction-status">
                            {workerInstructionStatusLabel(status)}
                          </span>
                        )}
                      </Show>
                      <Show when={msg.needs_reply}>
                        <span class="den-worker-fact-badge">Needs reply</span>
                      </Show>
                      <Show when={msg.sender_role === "worker" && !msg.delivered}>
                        <span class="den-worker-fact-badge">Unread</span>
                      </Show>
                    </div>
                    <p class="den-worker-history-body">{msg.body}</p>
                  </li>
                )}
              </For>
            </ul>
          </section>
        </Show>
        <Show when={rounds().length > 0}>
          <section class="den-worker-history-group">
            <h4 class="den-worker-transcript-coordination-heading">Rounds</h4>
            <ul class="den-worker-history-list">
              <For each={rounds()}>
                {(round) => {
                  const disp = () => workerRoundDisposition(round, props.worker().agent_type);
                  const canToggleEarlier = () =>
                    Boolean(round.result_handle && round.result_handle !== props.worker().result_handle);
                  const summaryText = () =>
                    round.result?.summary?.trim() || round.failure?.message?.trim() || "";
                  return (
                    <li
                      class="den-worker-history-item"
                      classList={{ "den-worker-history-item--highlight": highlightedKey() === `round:${round.ordinal}` }}
                      data-testid="worker-history-round"
                      data-round-ordinal={round.ordinal}
                    >
                      <div class="den-worker-history-meta">
                        <span class="den-worker-history-sender">Round {round.ordinal}</span>
                        <Show when={round.status}>
                          {(st) => <span class="den-worker-fact-badge">{formatSentenceCase(st())}</span>}
                        </Show>
                        <Show when={disp()} keyed>
                          {(d) => (
                            <span
                              class="den-worker-fact-badge"
                              data-testid="worker-round-disposition"
                              data-disposition={d.disposition}
                            >
                              {d.label}
                            </span>
                          )}
                        </Show>
                        <Show when={canToggleEarlier()}>
                          <button
                            type="button"
                            class="den-worker-history-action"
                            data-testid={`worker-round-summary-toggle-${round.ordinal}`}
                            aria-expanded={Boolean(expandedSummaries()[round.ordinal])}
                            onClick={() => toggleSummary(round.ordinal)}
                          >
                            {expandedSummaries()[round.ordinal] ? "Hide summary" : "Show summary"}
                          </button>
                        </Show>
                      </div>
                      <Show when={round.review_of}>
                        {(reviewHandle) => (
                          <p class="den-worker-reviewed-subject" data-testid="worker-round-reviewed-subject">
                            Reviewed result · <code>{reviewHandle()}</code>
                          </p>
                        )}
                      </Show>
                      <Show when={summaryText()}>
                        {(text) => <p class="den-worker-history-body">{text()}</p>}
                      </Show>
                      <Show when={canToggleEarlier() && expandedSummaries()[round.ordinal]}>
                        <WorkerSummarySection
                          inline
                          resultHandle={() => round.result_handle}
                          projectId={() => projectId() || undefined}
                        />
                      </Show>
                    </li>
                  );
                }}
              </For>
            </ul>
          </section>
        </Show>
      </Show>
      </WorkerTranscriptSection>
    </Show>
  );
}
