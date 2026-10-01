import { For, createMemo, createSignal } from "solid-js";
import type { SecurityFindingDataflow } from "../../api/types.ts";
import { formatLocation } from "../../lib/scan-display.ts";
import { dataflowSteps } from "../../lib/scan-dataflow.ts";
import { paginateSlice } from "../../list/pagination.ts";
import { TablePager } from "../list/TablePager.tsx";

export function FindingDataflow(props: { flow: SecurityFindingDataflow; repoRoot?: string | null }) {
  const [page, setPage] = createSignal(0);
  const steps = createMemo(() => dataflowSteps(props.flow));
  const pages = () => paginateSlice(steps(), page(), 20);
  return (
    <section aria-label="Data flow" data-testid="scans-dataflow">
      <dl class="den-scans-drill-grid">
        <For each={pages().slice}>{(step) => <>
          <dt>{step.kind}</dt>
          <dd>{formatLocation(step.location, props.repoRoot)}</dd>
        </>}</For>
      </dl>
      <TablePager page={pages().page} pageSize={20} total={steps().length} onPageChange={setPage} ariaLabel="Data flow steps" />
    </section>
  );
}
