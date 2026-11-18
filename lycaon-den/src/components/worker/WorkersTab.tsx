import { For, Show, createSignal, onCleanup } from "solid-js";
import type { Accessor } from "solid-js";
import type { BoardView, WorkerTask } from "../../api/types.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import {
  canCancelWorker,
  findSessionWorker,
  sessionWorkerRows,
  workerAgentLabel,
  workerDependencyLabel,
  workerRowTitleDisambiguated,
} from "../../chat/worker/workers-model.ts";
import { pendingWorkerApprovalByJob } from "../../chat/worker/worker-approval-model.ts";
import {
  workerDispositionPresentation,
  workerFactActivityLine,
  workerStateBadgeLabel,
  workerUnreadPresentation,
} from "../../chat/worker/worker-state-presentation.ts";
import { boardWorkerForTask } from "../../store/workers-board-sync.ts";
import { WorkerTurnsChip } from "./WorkerTurnsChip.tsx";
import { WorkerToolCallsChip } from "./WorkerToolCallsChip.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

type Props = {
  workers: WorkerTask[];
  board?: BoardView;
  selectedId: string | null;
  onSelect: (id: string) => void;
  onCancel: (id: string) => void;
  pendingCheckpoints?: readonly PendingCheckpoint[];
  /** Worker with an in-flight cancel request. */
  busyWorkerId?: Accessor<string | null>;
  /** Whether to include finished failures. */
  verboseMode?: boolean;
};

const ACK_MS = 280;

export function WorkersTab(props: Props) {
  const rows = () =>
    sessionWorkerRows(props.workers, { verboseMode: props.verboseMode ?? false });
  const pendingApprovals = () =>
    pendingWorkerApprovalByJob(props.workers, props.pendingCheckpoints ?? []);
  const busyWorkerId = () => props.busyWorkerId?.() ?? null;
  const resolvedSelectedId = () =>
    findSessionWorker(props.workers, props.selectedId)?.id ?? props.selectedId;
  const [ackId, setAckId] = createSignal<string | null>(null);
  let ackTimer: ReturnType<typeof setTimeout> | undefined;

  const selectWorker = (id: string) => {
    if (ackTimer) clearTimeout(ackTimer);
    setAckId(id);
    props.onSelect(id);
    ackTimer = setTimeout(() => {
      setAckId(null);
      ackTimer = undefined;
    }, ACK_MS);
  };

  onCleanup(() => {
    if (ackTimer) clearTimeout(ackTimer);
  });

  /** The list row is a snapshot; the chips read the live roster entry. */
  const liveRow = (row: WorkerTask) =>
    props.workers.find((candidate) => candidate.id === row.id) ?? row;

  const rowStateDetail = (live: WorkerTask) => {
    const bw = boardWorkerForTask(props.board, live);
    if (!bw && !live.parked_reason) return undefined;
    if (
      bw?.state === "asking" ||
      bw?.reason === "parked" ||
      Boolean(live.parked_reason) ||
      bw?.state === "recovering" ||
      bw?.state === "failed"
    ) {
      return workerFactActivityLine(live, bw) ?? workerStateBadgeLabel(live, bw);
    }
    return undefined;
  };

  return (
    <div class="den-workers-tab" data-testid="workers-tab">
      <Show
        when={rows().length > 0}
        fallback={
          <p class="den-workers-tab-empty" role="status" data-testid="workers-tab-empty">
            No workers in this session yet.
          </p>
        }
      >
        <Scrollport class="den-workers-tab-list" contentAs="ul" contentClass="den-workers-tab-list__content">
          <For each={rows()}>
            {(worker) => {
              const live = () => liveRow(worker);
              const bw = () => boardWorkerForTask(props.board, live());
              const disposition = () => workerDispositionPresentation(live(), bw());
              const unread = () => workerUnreadPresentation(bw());
              return (
                <li
                  class="den-workers-tab-row"
                  classList={{
                    "den-workers-tab-row--ack": ackId() === worker.id,
                    "den-workers-tab-row--approval": pendingApprovals().has(worker.id),
                  }}
                  data-testid="workers-tab-row"
                >
                  <button
                    type="button"
                    class="den-workers-tab-select"
                    aria-pressed={resolvedSelectedId() === worker.id}
                    onClick={() => selectWorker(worker.id)}
                  >
                    <span class="den-workers-tab-agent">
                      {live().worker_name?.trim() &&
                      live().worker_name?.trim() !== workerAgentLabel(worker)
                        ? `${live().worker_name?.trim()} · ${workerAgentLabel(worker)}`
                        : workerAgentLabel(worker)}
                    </span>
                    <span class="den-workers-tab-title">
                      {workerRowTitleDisambiguated(worker, props.workers)}
                    </span>
                    <Show when={pendingApprovals().has(worker.id)}>
                      <span
                        class="den-workers-tab-approval"
                        data-testid="workers-tab-approval"
                      >
                        Waiting for your approval
                      </span>
                    </Show>
                    <Show when={workerDependencyLabel(live())}>
                      {(label) => <span class="den-workers-tab-approval">{label()}</span>}
                    </Show>
                    <Show when={!pendingApprovals().has(worker.id) && rowStateDetail(live())}>
                      {(detail) => (
                        <span
                          class="den-workers-tab-approval"
                          data-testid="workers-tab-state-detail"
                        >
                          {detail()}
                        </span>
                      )}
                    </Show>
                    <Show when={disposition()} keyed>
                      {(disp) => (
                        <span
                          class="den-worker-fact-badge"
                          data-testid="worker-disposition-badge"
                          data-disposition={disp.disposition}
                        >
                          {disp.label}
                        </span>
                      )}
                    </Show>
                    <Show when={unread()} keyed>
                      {(u) => (
                        <span
                          class="den-worker-fact-badge"
                          data-testid="worker-unread-badge"
                          data-unread-count={u.count}
                        >
                          {u.label}
                        </span>
                      )}
                    </Show>
                    <WorkerTurnsChip worker={live} />
                    <WorkerToolCallsChip worker={live} />
                  </button>
                  <Show when={canCancelWorker(worker)}>
                    <button
                      type="button"
                      class="den-workers-tab-cancel"
                      classList={{
                        "den-workers-tab-cancel--busy": busyWorkerId() === worker.id,
                      }}
                      disabled={busyWorkerId() === worker.id}
                      aria-label={`Cancel ${workerAgentLabel(worker)} worker`}
                      data-testid={`workers-tab-cancel-${worker.id}`}
                      onClick={() => props.onCancel(worker.id)}
                    >
                      {busyWorkerId() === worker.id ? "Cancelling…" : "Cancel"}
                    </button>
                  </Show>
                </li>
              );
            }}
          </For>
        </Scrollport>
      </Show>
    </div>
  );
}
