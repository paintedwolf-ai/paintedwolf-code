import type { ActionDescriptor } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import { TOOL_ACTION_LABELS } from "../tool/tool-presentation.generated.ts";
import { mergeActions, type CardAction } from "./action-merge.ts";

/**
 * The action a card draws for a tool part: a folded session's merged
 * descriptor, a command row's merged member activities, or the part's own.
 */
export function partAction(part: ToolPartView): CardAction | undefined {
  if (part.foldMembers?.length) return mergeActions([part, ...part.foldMembers].flatMap((member) => member.action ?? []));
  const activity = part.commandActivity;
  if (activity) {
    const members = activity.members.flatMap((member) => member.action ?? []);
    const calls = activity.calls.flatMap((call) => (call.commandProgram?.activities?.length ? [] : call.action ?? []));
    return mergeActions(members.length ? [...members, ...calls.filter((action) => !members.includes(action))] : calls);
  }
  return part.action;
}

/**
 * A call the host has not described yet, such as a command before its first
 * activity: its catalog name, its title and its status, with no facts.
 */
export function pendingAction(part: ToolPartView): CardAction {
  return {
    label: TOOL_ACTION_LABELS[part.tool.toLowerCase()] ?? part.tool,
    status: part.status === "completed" ? "done" : part.status,
    ran: { text: part.title?.trim() || part.tool, noun: "target" },
    facts: [],
    row: [],
    links: part.output ? [{ kind: "input" }, { kind: "output" }] : [{ kind: "input" }],
  };
}

function foldKey(entry: ActivitySpanEntry): string {
  return entry.kind === "tool" ? entry.part.action?.fold_key ?? "" : "";
}

/** Adjacent calls the host gave one fold key become one card on the first call's row. */
export function foldActionEntries(entries: readonly ActivitySpanEntry[]): ActivitySpanEntry[] {
  const out: ActivitySpanEntry[] = [];
  for (const entry of entries) {
    const previous = out.at(-1);
    const key = foldKey(entry);
    if (entry.kind === "tool" && key && previous?.kind === "tool" && foldKey(previous) === key) {
      const members = [...(previous.part.foldMembers ?? []), entry.part];
      const merged: ActionDescriptor | undefined = mergeActions([previous.part, ...members].flatMap((part) => part.action ?? []));
      out[out.length - 1] = { ...previous, part: { ...previous.part, foldMembers: members, status: merged?.status === "running" ? "running" : merged?.status === "error" ? "error" : "completed" } };
      continue;
    }
    out.push(entry);
  }
  return out;
}
