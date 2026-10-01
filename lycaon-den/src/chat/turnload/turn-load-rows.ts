import type { Message, TurnLoad, TurnLoadBoundary, TurnLoadTool } from "../../api/types.ts";
import { formatDuration } from "../../time/time-copy.ts";
import { isInternalTranscriptUserMessage } from "../transcript/projection/message-transcript.ts";

/** A turn decision drawn in the tool activity span it established. */
export type TurnLoadRow = {
  key: string;
  role: "tools";
  load: TurnLoad;
  /** Assistant message whose activity span the row joins. */
  assistantMessageId: string;
  /** Message where the row is ordered. */
  anchorMessageId: string;
  workflowRunId?: string;
  /** Structural order before the tool rows. */
  sub: number;
};

/** Tool rows sit at TOOL_SUB_ORDINAL_BASE (10) plus their call index. */
const TURN_ROW_SUB = 8;

const rowKey = (load: TurnLoad, role: TurnLoadRow["role"]): string =>
  `turn-load:${load.opening_message_id}:${load.trigger}:${load.tool_call_id ?? ""}:${role}`;

function orderMessages(messages: readonly Message[]): Message[] {
  return [...messages].sort(
    (a, b) =>
      (a.ord ?? Number.MAX_SAFE_INTEGER) - (b.ord ?? Number.MAX_SAFE_INTEGER) ||
      a.id.localeCompare(b.id),
  );
}

/** A person's prompt opens a turn the way the host's turn ledger keys it. */
export function messageOpensLoadTurn(message: Message): boolean {
  return message.role === "user" && message.origin === "user" && !isInternalTranscriptUserMessage(message);
}

function hasLiveToolCalls(message: Message): boolean {
  return message.role === "assistant" && (message.tool_calls?.length ?? 0) > 0 && message.draft_status !== "withdrawn";
}

/** Where each turn's first tool activity and each model call live. */
type TurnAnchors = {
  firstToolAssistantByOpening: Map<string, Message>;
};

function indexTurnAnchors(messages: readonly Message[]): TurnAnchors {
  const firstToolAssistantByOpening = new Map<string, Message>();
  let opening: string | undefined;
  for (const message of orderMessages(messages)) {
    if (messageOpensLoadTurn(message)) {
      opening = message.id;
      continue;
    }
    if (message.role !== "assistant") continue;
    if (opening && hasLiveToolCalls(message) && !firstToolAssistantByOpening.has(opening)) {
      firstToolAssistantByOpening.set(opening, message);
    }
  }
  return { firstToolAssistantByOpening };
}

/** Rows for every decided receipt whose turn reached a tool call. */
export function turnLoadRows(
  messages: readonly Message[],
  turnLoads: Readonly<Record<string, readonly TurnLoad[]>> | undefined,
): TurnLoadRow[] {
  if (!turnLoads) return [];
  const rows: TurnLoadRow[] = [];
  let anchors: TurnAnchors | undefined;
  const anchorIndex = () => (anchors ??= indexTurnAnchors(messages));
  for (const loads of Object.values(turnLoads)) {
    for (const load of loads) {
      if (load.abstained) continue;
      if (load.trigger === "turn") {
        const anchor = anchorIndex().firstToolAssistantByOpening.get(load.opening_message_id);
        if (!anchor) continue;
        const base = {
          load,
          assistantMessageId: anchor.id,
          anchorMessageId: anchor.id,
          workflowRunId: anchor.workflow_run_id?.trim() || undefined,
        };
        rows.push({ ...base, key: rowKey(load, "tools"), role: "tools", sub: TURN_ROW_SUB });
        continue;
      }
    }
  }
  return rows;
}

/** What the transcript calls the engine; its structured name stays on the receipt. */
export const LOCAL_AI_LABEL = "Local AI";

/** The engine's structured name, the row's tool identity. */
export function turnLoadEngineName(load: TurnLoad): string {
  return load.engine?.name?.trim() || "decision";
}

export function formatProbability(p: number): string {
  return p.toFixed(2).replace(/^0(?=\.)/, "");
}

/** What the collapsed row says: the row's subject, and its operation and state. */
export function turnLoadRowSummary(row: TurnLoadRow): { title: string; operation: string; state: string } {
  const elapsed = `${row.load.elapsed_ms} ms`;
  const tools = row.load.tools.map((tool) => tool.tool);
  const subjects = [...tools];
  if (row.load.preloaded_skill) subjects.push(`skill: ${row.load.preloaded_skill.name}`);
  if (subjects.length > 0) {
    return { title: subjects.join(", "), operation: tools.length > 0 ? "offered" : "preloaded", state: elapsed };
  }
  const omitted = row.load.guides?.omitted ?? 0;
  const title = omitted > 0
    ? `floor only · ${omitted} ${omitted === 1 ? "guide" : "guides"} left out`
    : "floor only";
  return { title, operation: row.load.kind?.value ?? "decided", state: elapsed };
}

/** How one standing tool reads in the card: its P when a decision offered it, or the request that loaded it. */
export function turnLoadToolDetail(tool: TurnLoadTool): string {
  if (tool.source === "requested") return "requested";
  if (tool.source === "companion") return tool.with ? `with ${tool.with}` : "companion";
  return tool.p != null ? formatProbability(tool.p) : "";
}

/**
 * What the host knew about the provider's prompt cache when the turn opened:
 * a warm turn kept the standing tools and only added to them, a cold one
 * chose them again.
 */
export function turnLoadBoundaryCopy(boundary: TurnLoadBoundary): string {
  if (boundary.cache === "warm") {
    const idle = boundary.idle_ms != null ? ` · idle ${formatDuration(boundary.idle_ms)}` : "";
    return `Warm${idle} · earlier tools kept`;
  }
  return `Cold · ${coldReasonCopy(boundary)} · tools chosen again`;
}

function coldReasonCopy(boundary: TurnLoadBoundary): string {
  switch (boundary.reason) {
    case "first_turn":
      return "first turn";
    case "uncached":
      return "this provider keeps no prompt cache";
    case "idle": {
      const idle = formatDuration(boundary.idle_ms ?? 0);
      return boundary.cold_after_ms != null
        ? `idle ${idle}, past the ${formatDuration(boundary.cold_after_ms)} the cache keeps`
        : `idle ${idle}`;
    }
    case "model_changed":
      return "model changed";
    case "not_resident":
      return "model unloaded";
    default:
      return "prompt cache gone";
  }
}
