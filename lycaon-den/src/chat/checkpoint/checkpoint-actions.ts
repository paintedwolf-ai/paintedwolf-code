import type { LycaonClient } from "../../api/client.ts";
import type { ContentApplyDecision, ToolApprovalResolveBody } from "../../api/types.ts";
import type { PromptSecretReferencePart } from "../../api/types.ts";
import type { PendingCheckpoint } from "./checkpoint-model.ts";

export async function resolveToolApproval(
  client: LycaonClient,
  checkpoint: PendingCheckpoint,
  action: "approve" | "reject",
  options?: {
    optionId?: string;
    guidance?: string;
    secrets?: PromptSecretReferencePart[];
  },
): Promise<void> {
  let body: ToolApprovalResolveBody;
  if (action === "approve") {
    if (!options?.optionId) throw new Error("Choose how to approve this request.");
    body = { kind: "tool_approval", action, option_id: options.optionId };
  } else {
    body = {
      kind: "tool_approval",
      action,
      ...(options?.guidance ? { guidance: options.guidance } : {}),
      ...(options?.secrets?.length ? { secrets: options.secrets } : {}),
    };
  }
  await client.resolveCheckpoint(checkpoint.sessionId, checkpoint.checkpointId, body);
}

export async function resolveContentApply(
  client: LycaonClient,
  checkpoint: PendingCheckpoint,
  decision: ContentApplyDecision,
  opts?: {
    approvedHunks?: string[];
    guidance?: string;
    secrets?: PromptSecretReferencePart[];
  },
): Promise<void> {
  await client.resolveCheckpoint(
    checkpoint.sessionId,
    checkpoint.checkpointId,
    {
      kind: "content_apply",
      decision,
      ...(opts?.approvedHunks?.length
        ? { approved_hunks: opts.approvedHunks }
        : {}),
      ...(opts?.guidance && decision === "reject"
        ? { guidance: opts.guidance }
        : {}),
      ...(opts?.secrets?.length && decision === "reject" ? { secrets: opts.secrets } : {}),
    },
  );
}
