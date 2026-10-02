import type { LycaonClient } from "../../api/client.ts";
import type { ContentApplyDecision, ToolApprovalResolveBody } from "../../api/types.ts";
import type { PromptSecretReferencePart } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { resolveCheckpointWithPresence } from "../../platform/presence.ts";
import { chatUnlocked } from "../vault/chat-vault-store.ts";
import type { PendingCheckpoint } from "./checkpoint-model.ts";

/**
 * Whether choosing optionId would send values the person stored while their
 * chat is locked, which only the desktop shell can approve after confirming
 * them. While the chat is unlocked, approving is an ordinary choice.
 */
export function optionNeedsPresence(checkpoint: PendingCheckpoint, optionId: string): boolean {
  const plan = checkpoint.tool_approval?.plan;
  if (!plan?.held_release || chatUnlocked(plan.held_release.chat_session_id)) return false;
  return plan.options.some((option) => option.id === optionId && option.decision_action === "approve");
}

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
    if (optionNeedsPresence(checkpoint, options.optionId)) {
      await resolveCheckpointWithPresence(checkpoint.sessionId, checkpoint.checkpointId, options.optionId);
      return;
    }
    body = { kind: "tool_approval", action, option_id: options.optionId };
    try {
      await client.resolveCheckpoint(checkpoint.sessionId, checkpoint.checkpointId, body);
    } catch (error) {
      // The chat locked after this view last heard it was unlocked.
      if (!(error instanceof LycaonApiError) || error.code !== "presence_required") throw error;
      await resolveCheckpointWithPresence(checkpoint.sessionId, checkpoint.checkpointId, options.optionId);
    }
    return;
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
