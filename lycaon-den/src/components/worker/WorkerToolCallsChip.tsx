import { Show, type Accessor } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import {
  formatWorkerToolCalls,
  workerToolCallsAriaLabel,
} from "../../chat/worker/worker-progress.ts";

type Props = {
  worker: Accessor<WorkerTask>;
  testId?: string;
};

/** Tool calls have no budget ceiling. */
export function WorkerToolCallsChip(props: Props) {
  const label = () => formatWorkerToolCalls(props.worker());
  return (
    <Show when={label()}>
      {(text) => (
        <span
          class="den-worker-tool-calls"
          data-testid={props.testId ?? "worker-tool-calls"}
          aria-label={workerToolCallsAriaLabel(props.worker())}
        >
          {text()}
        </span>
      )}
    </Show>
  );
}
