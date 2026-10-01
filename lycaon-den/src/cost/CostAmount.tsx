import { Show, createMemo } from "solid-js";
import type { CostSummary } from "../api/types.ts";
import { estimateView, joinReasons } from "./cost-format.ts";

type Props = {
  summary: CostSummary;
  /** Text shown in the amount slot when nothing is priced. */
  unpricedText: string;
  /** Test id for the amount slot; the coverage mark takes `${testId}-coverage`. */
  testId?: string;
};

/** Coverage renders beside the amount. */
export function CostAmount(props: Props) {
  const view = createMemo(() => estimateView(props.summary));
  return (
    <span class="den-cost-amount" data-coverage={view().coverage}>
      <span data-testid={props.testId}>{view().amount ?? props.unpricedText}</span>
      <Show when={view().coverage === "lower_bound"}>
        <span
          class="den-status-mark"
          data-tone="warning"
          data-testid={props.testId ? `${props.testId}-coverage` : undefined}
          data-tip={`Actual charges may be higher: ${joinReasons(view().reasons)}`}
        >
          {view().qualifier}
        </span>
      </Show>
    </span>
  );
}
