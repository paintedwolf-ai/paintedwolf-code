import type { Message, TurnLoad, TurnLoadMatchBy } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import { messageOpensLoadTurn } from "./turn-load-rows.ts";

/** The load fact a tool card states at its foot. */
export type ToolLoadFact =
  | { kind: "offered"; p: number }
  | { kind: "kept" }
  /** The tool loaded as a declared companion of a predicted tool. */
  | { kind: "companion"; with: string }
  | { kind: "requested"; need: string }
  | {
      kind: "matched";
      need: string;
      by: TurnLoadMatchBy;
      /** The tools or skills the text resolved to, best first. */
      names: readonly string[];
      elapsedMs: number;
      /** True when the turn had a decision and it had not offered what the request loaded. */
      missed: boolean;
    }
  | {
      /** This call was the turn's first loadable tool, and the local AI chose a skill for it. */
      kind: "skill";
      name: string;
      /** True when the skill's body was read into the turn; false when it was only named. */
      read: boolean;
      elapsedMs: number;
    };

/** Receipts reachable from a tool card: by the turn its assistant row belongs to, and by call. */
export type TurnLoadIndex = {
  turnByAssistantMessageId: ReadonlyMap<string, TurnLoad>;
  requestsByOpening: ReadonlyMap<string, readonly TurnLoad[]>;
  byToolCallId: ReadonlyMap<string, TurnLoad>;
};

const EMPTY_INDEX: TurnLoadIndex = {
  turnByAssistantMessageId: new Map(),
  requestsByOpening: new Map(),
  byToolCallId: new Map(),
};

export function buildTurnLoadIndex(
  messages: readonly Message[],
  turnLoads: Readonly<Record<string, readonly TurnLoad[]>> | undefined,
): TurnLoadIndex {
  if (!turnLoads || Object.keys(turnLoads).length === 0) return EMPTY_INDEX;
  const turnByOpening = new Map<string, TurnLoad>();
  const requestsByOpening = new Map<string, TurnLoad[]>();
  const byToolCallId = new Map<string, TurnLoad>();
  for (const loads of Object.values(turnLoads)) {
    for (const load of loads) {
      if (load.trigger === "turn") turnByOpening.set(load.opening_message_id, load);
      if (load.tool_call_id) byToolCallId.set(load.tool_call_id, load);
      if (load.trigger === "request" && !load.abstained) {
        const list = requestsByOpening.get(load.opening_message_id) ?? [];
        list.push(load);
        requestsByOpening.set(load.opening_message_id, list);
      }
    }
  }
  const ordered = [...messages].sort(
    (a, b) => (a.ord ?? Number.MAX_SAFE_INTEGER) - (b.ord ?? Number.MAX_SAFE_INTEGER) || a.id.localeCompare(b.id),
  );
  const turnByAssistantMessageId = new Map<string, TurnLoad>();
  let opening: string | undefined;
  for (const message of ordered) {
    if (messageOpensLoadTurn(message)) {
      opening = message.id;
      continue;
    }
    if (message.role !== "assistant" || !opening) continue;
    const turn = turnByOpening.get(opening);
    if (turn) turnByAssistantMessageId.set(message.id, turn);
  }
  return { turnByAssistantMessageId, requestsByOpening, byToolCallId };
}

const RESOLVING_TOOLS: ReadonlySet<string> = new Set(["request_tools", "skills_read"]);

/** The fact for one tool card, or nothing when no decision touched it. */
export function toolLoadFact(index: TurnLoadIndex, part: Pick<ToolPartView, "tool" | "toolCallId" | "assistantMessageId">): ToolLoadFact | undefined {
  const turn = index.turnByAssistantMessageId.get(part.assistantMessageId.trim());
  if (RESOLVING_TOOLS.has(part.tool)) {
    const receipt = index.byToolCallId.get(part.toolCallId.trim());
    const match = receipt?.match;
    if (!receipt || !match) return undefined;
    const offered = new Set((turn?.tools ?? []).map((tool) => tool.tool));
    const missed = turn != null && !turn.abstained && part.tool === "request_tools" && match.names.some((name) => !offered.has(name));
    return {
      kind: "matched",
      need: match.need,
      by: match.by,
      names: match.names,
      elapsedMs: receipt.elapsed_ms,
      missed,
    };
  }
  const event = index.byToolCallId.get(part.toolCallId.trim());
  const chosen = event?.trigger === "tool_event" ? event.match?.names[0] : undefined;
  if (event && chosen) {
    return { kind: "skill", name: chosen, read: event.preloaded_skill != null, elapsedMs: event.elapsed_ms };
  }
  if (turn && !turn.abstained) {
    const offered = turn.tools.find((tool) => tool.tool === part.tool);
    if (offered?.carried) return { kind: "kept" };
    if (offered?.source === "companion" && offered.with) return { kind: "companion", with: offered.with };
    if (offered?.p != null) return { kind: "offered", p: offered.p };
    for (const request of index.requestsByOpening.get(turn.opening_message_id) ?? []) {
      if (request.match?.names.includes(part.tool)) return { kind: "requested", need: request.match.need };
    }
  }
  return undefined;
}
