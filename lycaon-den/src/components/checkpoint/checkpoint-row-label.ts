import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import {
  approvalCommand,
  approvalHeadline,
  approvalIdentity,
  approvalToolName,
} from "../../chat/checkpoint/approval-display.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";

/** Labels both queue rows and minimized cards. */
export function checkpointRowLabel(checkpoint: PendingCheckpoint): string {
  const copy = APPROVALS_COPY.card.action;
  const join = (action: string, subject?: string) => {
    const s = subject?.trim();
    return s ? `${action} · ${s}` : action;
  };
  switch (checkpoint.kind) {
    case "content_apply":
      return join(copy.applyChanges, checkpoint.content_apply?.path);
    default: {
      if (checkpoint.tool_approval?.plan.subject.kind === "secret") {
        return approvalIdentity(checkpoint.tool_approval);
      }
      const tool = approvalToolName(checkpoint.tool_approval);
      const action =
        tool && tool !== "command" ? copy.useTool(tool) : copy.runCommand;
      return join(
        action,
        approvalCommand(checkpoint.tool_approval) ||
          approvalHeadline(checkpoint.tool_approval),
      );
    }
  }
}
