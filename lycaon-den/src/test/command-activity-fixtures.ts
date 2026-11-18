import type { CommandActivity, CommandProgramReceipt } from "../api/types.ts";
import type { ToolPartView } from "../chat/tool/tool-part-model.ts";
import type { ActivitySpanEntry } from "../chat/transcript/projection/transcript-item-model.ts";

export function activityFact(id: string, fields: Partial<CommandActivity> = {}): CommandActivity {
  return {
    id, group: "Reading files", phase: "inspect", label: "Read files", subject: id,
    state: "completed", outcome: "Completed", execution_ids: [id], spans: [], ...fields,
  };
}

export function activityProgram(id: string, fields: Partial<CommandProgramReceipt> = {}): CommandProgramReceipt {
  return {
    id: `program-${id}`, parent_receipt_id: `parent-${id}`, state: "completed",
    language_version: 1, catalog_digest: "catalog", source_digest: "source", source_ref: "source",
    detail_ref: "detail", progress_state: "unchanged", started_at: "2026-01-01T00:00:00Z", ...fields,
  };
}

export function activityTool(id: string, tool: string, fields: Partial<ToolPartView> = {}): Extract<ActivitySpanEntry, { kind: "tool" }> {
  return {
    kind: "tool",
    part: { id, toolCallId: id, assistantMessageId: "assistant", messageId: `message-${id}`, tool, kind: "command", status: "completed", ...fields },
  };
}

export function outputRead(id: string, status: ToolPartView["status"] = "completed") {
  return activityTool(id, "command_output", { status, args: { process_id: "job" } });
}

export function activityDecision(key: string): Extract<ActivitySpanEntry, { kind: "turn_load" }> {
  return {
    kind: "turn_load",
    key,
    row: {
      key, role: "tools", assistantMessageId: "assistant", anchorMessageId: "assistant",
      load: {
        session_id: "session", trigger: "turn", opening_message_id: key, abstained: false,
        elapsed_ms: 1, floor: ["command"], tools: [],
      },
    },
  };
}
