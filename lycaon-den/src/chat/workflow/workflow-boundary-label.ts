import type { WorkflowBoundaryMeta, WorkflowRun } from "../../api/types.ts";
import { BUILD_POSTURE_LABEL } from "../../workflow/workflows-drawer-model.ts";
import { isAmbientRootRun } from "../../workflow/workflow-run-stack.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

export interface WorkflowBoundaryView {
  key: string;
  messageId: string;
  label: string;
}

/** Format a workflow id for chat chrome (`security_survey` → `Security survey`). */
function workflowDisplayName(
  workflowId: string | undefined,
  run?: WorkflowRun,
): string {
  if (isAmbientRootRun(run)) return BUILD_POSTURE_LABEL;
  const id = (workflowId ?? run?.workflow_id ?? "").trim();
  if (!id) return "Workflow";
  return formatSentenceCase(id.replaceAll("-", "_"));
}

/** Human boundary label for chat — name + event only (no version/phase soup). */
export function workflowBoundaryLabel(
  meta: WorkflowBoundaryMeta | undefined,
  run?: WorkflowRun,
): string {
  const event = meta?.event ?? "started";
  const name = workflowDisplayName(meta?.workflow_id, run);
  switch (event) {
    case "started":
      return `${name} started`;
    case "completed":
    case "exited":
      return `${name} finished`;
    case "canceled":
      return `${name} canceled`;
    case "failed":
      return `${name} failed`;
    case "interrupted":
      return `${name} interrupted`;
    case "paused":
      return `${name} paused`;
    // Parent yielded to a subroutine child — work continues; this is not a soft pause.
    case "paused_on_child":
      return `${name} waiting on nested work`;
    case "resumed":
      return `${name} resumed`;
    default:
      return `${name} ${event}`;
  }
}
