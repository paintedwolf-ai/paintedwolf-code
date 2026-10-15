import { Match, Switch, onMount } from "solid-js";
import { announce } from "../../platform/interaction/announcer.tsx";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { checkpointRowLabel } from "./checkpoint-row-label.ts";
import type { ApprovalCardProps } from "./approval-card-types.ts";
import { ToolApprovalBody } from "./ToolApprovalBody.tsx";
import { ContentApplyBody } from "./ContentApplyBody.tsx";

export function ApprovalCard(props: ApprovalCardProps) {
  const kind = () => props.checkpoint.kind;
  onMount(() => announce(`${APPROVALS_COPY.card.state.needsApproval}: ${checkpointRowLabel(props.checkpoint)}`));
  return (
    <Switch fallback={<ToolApprovalBody {...props} />}>
      <Match when={kind() === "content_apply"}>
        <ContentApplyBody {...props} />
      </Match>
    </Switch>
  );
}
