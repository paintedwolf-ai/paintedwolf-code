import type { PendingWorkflowStart, WorkflowSummary } from "../../api/types.ts";
import { catalogEntryForWorkflow } from "../../workflow/workflows-drawer-model.ts";
import { DenButton } from "../primitives/DenButton.tsx";

type Props = {
  proposal: PendingWorkflowStart;
  catalog: WorkflowSummary[];
  busy: boolean;
  onStart: () => void;
  onOpenDrawer: () => void;
  onDismiss: () => void;
};

export function WorkflowStartProposalCard(props: Props) {
  const name = () => {
    if (props.proposal.label?.trim()) return props.proposal.label.trim();
    const entry = catalogEntryForWorkflow(
      props.catalog,
      props.proposal.workflow_id,
      props.proposal.workflow_version,
      props.proposal.preset_id,
    );
    return entry?.name ?? props.proposal.workflow_id;
  };

  return (
    <div
      class="den-workflow-start-card"
      data-testid="workflow-start-proposal-card"
      role="region"
      aria-label="Workflow start proposal"
    >
      <p class="den-workflow-start-card-lead">
        Start <strong>{name()}</strong> workflow?
      </p>
      <p class="den-workflow-start-card-hint">
        Use Start, send <code>/plan</code>, or open the Workflows drawer.
      </p>
      <div class="den-workflow-start-card-actions">
        <DenButton
          variant="primary"
          compact
          data-testid="workflow-start-proposal-start"
          disabled={props.busy}
          onClick={() => props.onStart()}
        >
          Start
        </DenButton>
        <DenButton
          variant="ghost"
          data-testid="workflow-start-proposal-drawer"
          disabled={props.busy}
          onClick={() => props.onOpenDrawer()}
        >
          Open workflows
        </DenButton>
        <DenButton
          variant="ghost"
          data-testid="workflow-start-proposal-dismiss"
          disabled={props.busy}
          onClick={() => props.onDismiss()}
        >
          Dismiss
        </DenButton>
      </div>
    </div>
  );
}
