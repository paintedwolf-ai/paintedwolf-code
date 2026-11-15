import { Show, type Accessor } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import {
  formatWorkerTurns,
  workerTurnsAriaLabel,
} from "../../chat/worker/worker-progress.ts";

type Props = {
  worker: Accessor<WorkerTask>;
  testId?: string;
};

export function WorkerTurnsChip(props: Props) {
  const label = () => formatWorkerTurns(props.worker());
  return (
    <Show when={label()}>
      {(text) => (
        <span
          class="den-worker-turns"
          data-testid={props.testId}
          aria-label={workerTurnsAriaLabel(props.worker())}
        >
          {text()}
        </span>
      )}
    </Show>
  );
}
