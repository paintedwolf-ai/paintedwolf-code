import { For, Show, createSignal, onCleanup } from "solid-js";
import type { Accessor } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import {
  canCancelWorker,
  sessionWorkerRows,
  workerAgentLabel,
  workerDependencyLabel,
  workerRowTitleDisambiguated,
} from "../../chat/worker/workers-model.ts";
import { pendingWorkerApprovalByJob } from "../../chat/worker/worker-approval-model.ts";
import { WorkerBudgetChip } from "./WorkerBudgetChip.tsx";
import { WorkerToolCallsChip } from "./WorkerToolCallsChip.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

type Props = {
  workers: WorkerTask[];
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

  return (
    <div class="den-workers-tab" data-testid="workers-tab">
      <Show
        when={rows().length > 0}
        fallback={
          <p class="den-workers-tab-empty" role="status">
            No workers in this session yet.
          </p>
        }
      >
        <Scrollport class="den-workers-tab-list" contentAs="ul" contentClass="den-workers-tab-list__content">
          <For each={rows()}>
            {(worker) => (
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
                  aria-pressed={props.selectedId === worker.id}
                  onClick={() => selectWorker(worker.id)}
                >
                  <span class="den-workers-tab-agent">{workerAgentLabel(worker)}</span>
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
                  <Show when={workerDependencyLabel(liveRow(worker))}>
                    {(label) => <span class="den-workers-tab-approval">{label()}</span>}
                  </Show>
                  <WorkerBudgetChip worker={() => liveRow(worker)} />
                  <WorkerToolCallsChip worker={() => liveRow(worker)} />
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
                    onClick={() => props.onCancel(worker.id)}
                  >
                    {busyWorkerId() === worker.id ? "Cancelling…" : "Cancel"}
                  </button>
                </Show>
              </li>
            )}
          </For>
        </Scrollport>
      </Show>
    </div>
  );
}
