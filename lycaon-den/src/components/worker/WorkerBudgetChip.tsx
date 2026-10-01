import { Show, type Accessor } from "solid-js";
import type { WorkerTask } from "../../api/types.ts";
import {
  formatWorkerBudget,
  isWorkerBudgetLow,
  workerBudgetAriaLabel,
} from "../../chat/worker/worker-progress.ts";

type Props = {
  worker: Accessor<WorkerTask>;
  testId?: string;
};

export function WorkerBudgetChip(props: Props) {
  const label = () => formatWorkerBudget(props.worker());
  return (
    <Show when={label()}>
      {(text) => (
        <span
          class="den-worker-budget"
          classList={{ "den-worker-budget--low": isWorkerBudgetLow(props.worker()) }}
          data-testid={props.testId}
          aria-label={workerBudgetAriaLabel(props.worker())}
        >
          {text()}
        </span>
      )}
    </Show>
  );
}
