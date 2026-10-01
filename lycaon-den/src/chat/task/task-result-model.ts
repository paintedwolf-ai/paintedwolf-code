import type { Message } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

function isWorkerDispatchTool(toolName: string): boolean {
  const n = toolName.toLowerCase();
  return n === "task" || n === "delegate_dispatch";
}

export function taskJobIdFromToolMessage(msg: Message): string | undefined {
  if (msg.role !== "tool") return undefined;
  const dispatch = msg.tool_result?.dispatch;
  return dispatch?.worker_id.trim() || undefined;
}

export function taskJobIdFromPart(part: ToolPartView): string | undefined {
  return part.jobId?.trim() || undefined;
}

export function isEnqueuedWorkerDispatchResult(
  toolName: string,
  resultMsg: Message | undefined,
): boolean {
  if (!isWorkerDispatchTool(toolName) || !resultMsg) return false;
  return taskJobIdFromToolMessage(resultMsg) != null;
}
