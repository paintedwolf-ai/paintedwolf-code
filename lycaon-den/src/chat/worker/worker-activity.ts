import type { Message } from "../../api/types.ts";
import { resolveChickletTitle } from "../tool/tool-chicklet-titles.ts";
import { truncateTitle } from "../tool/tool-title-format.ts";

/** Activity labels use the latest call name and arguments. */
export function lastWorkerToolActivity(messages: readonly Message[]): string | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg?.role !== "assistant" || !msg.tool_calls?.length) continue;
    for (let j = msg.tool_calls.length - 1; j >= 0; j--) {
      const call = msg.tool_calls[j];
      const tool = call?.name?.trim();
      if (!call || !tool) continue;
      const title = resolveChickletTitle(tool, call.args);
      return title ? truncateTitle(`${tool} · ${title}`, 96) : tool;
    }
  }
  return null;
}
