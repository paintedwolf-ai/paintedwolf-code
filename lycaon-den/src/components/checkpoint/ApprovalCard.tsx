import { Match, Switch } from "solid-js";
import type { ApprovalCardProps } from "./approval-card-types.ts";
import { ToolApprovalBody } from "./ToolApprovalBody.tsx";
import { ContentApplyBody } from "./ContentApplyBody.tsx";

export function ApprovalCard(props: ApprovalCardProps) {
  const kind = () => props.checkpoint.kind;
  return (
    <Switch fallback={<ToolApprovalBody {...props} />}>
      <Match when={kind() === "content_apply"}>
        <ContentApplyBody {...props} />
      </Match>
    </Switch>
  );
}
