import type { Message, TurnLoad, WorkerTask, WorkflowRun } from "../../api/types.ts";
import {
  isAmbientRootRun,
  resolveLeafRun,
  runsByIdMap,
} from "../../workflow/workflow-run-stack.ts";
import { messagesToTranscriptItems } from "../transcript/projection/transcript-items.ts";
import { sortTranscriptItemsByOrd } from "../transcript/projection/transcript-item-order.ts";
import { type RawTranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import {
  checkpointToolCallId,
  type PendingCheckpoint,
} from "../checkpoint/checkpoint-model.ts";
import { checkpointMetaFromPending } from "../checkpoint/approval-display.ts";

export type ChatSpanBlock = {
  kind: "run";
  runId: string;
  startMessageId?: string;
  items: RawTranscriptItem[];
  /** Ambient run; drawn with minimal transcript chrome. */
  ambientSpan?: boolean;
};

/** Catalog spans draw visible workflow boundaries and edge padding. */
export function isCatalogSpan(block: ChatSpanBlock): boolean {
  return block.runId !== "" && block.ambientSpan !== true;
}

function transcriptItemsForMessages(
  messages: readonly Message[],
  options?: ChatSpanBuildOptions,
): RawTranscriptItem[] {
  return messagesToTranscriptItems(messages, {
    verboseMode: options?.verboseMode ?? false,
    layout: "chat",
    turnLoads: options?.turnLoads,
  });
}

function runsByIdWithLeaf(
  runsById: ReadonlyMap<string, WorkflowRun>,
  leafRun?: WorkflowRun,
): Map<string, WorkflowRun> {
  const map = new Map(runsById);
  if (leafRun?.id) map.set(leafRun.id, leafRun);
  return map;
}

/** Returns the first message ordinal in a span. */
function bucketMinOrd(messages: readonly Message[]): number {
  let min = Number.MAX_SAFE_INTEGER;
  for (const msg of messages) {
    const ord = msg.ord ?? 0;
    if (ord < min) min = ord;
  }
  return min === Number.MAX_SAFE_INTEGER ? 0 : min;
}

function spanBlockForRun(
  runId: string,
  items: RawTranscriptItem[],
  runsById: ReadonlyMap<string, WorkflowRun>,
): ChatSpanBlock {
  const run = runsById.get(runId);
  return {
    kind: "run",
    runId,
    startMessageId: run?.start_message_id,
    items,
    ambientSpan: isAmbientRootRun(run),
  };
}

/** Groups messages by workflow run and orders spans by message ordinal. */
function buildSpanBlocks(
  messages: readonly Message[],
  runsById: ReadonlyMap<string, WorkflowRun>,
  options?: ChatSpanBuildOptions,
): ChatSpanBlock[] {
  const bucketsByRun = new Map<string, Message[]>();

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (!msg) continue;
    const runId = msg.workflow_run_id?.trim() ?? "";
    const bucket = bucketsByRun.get(runId);
    if (bucket) {
      bucket.push(msg);
    } else {
      bucketsByRun.set(runId, [msg]);
    }
  }

  const orderedRunIds = [...bucketsByRun.keys()].sort((a, b) => {
    const aMin = bucketMinOrd(bucketsByRun.get(a) ?? []);
    const bMin = bucketMinOrd(bucketsByRun.get(b) ?? []);
    return aMin - bMin;
  });

  return orderedRunIds.map((runId) => {
    const bucket = bucketsByRun.get(runId) ?? [];
    return spanBlockForRun(
      runId,
      transcriptItemsForMessages(bucket, options),
      runsById,
    );
  });
}

export type ChatSpanBuildOptions = {
  verboseMode?: boolean;
  workers?: readonly WorkerTask[];
  pendingCheckpoints?: readonly PendingCheckpoint[];
  /** Decision-engine receipts keyed by the user message that opened their turn. */
  turnLoads?: Readonly<Record<string, readonly TurnLoad[]>>;
};

function assistantAnchorForToolCall(
  messages: readonly Message[],
  toolCallId: string,
): Message | undefined {
  for (const msg of messages) {
    if (
      msg.role === "assistant" &&
      msg.tool_calls?.some((call) => call.id?.trim() === toolCallId)
    ) {
      return msg;
    }
  }
  for (const msg of messages) {
    const result = msg.tool_result;
    if (result?.tool_call_id?.trim() !== toolCallId) continue;
    const assistantId = result.assistant_message_id?.trim();
    if (!assistantId) continue;
    return messages.find((candidate) => candidate.id === assistantId);
  }
  return undefined;
}

function parentAnchorForChildCheckpoint(
  messages: readonly Message[],
  workers: readonly WorkerTask[],
  checkpoint: PendingCheckpoint,
): Message | undefined {
  const childId = checkpoint.sessionId.trim();
  if (!childId) return undefined;
  const jobIds = new Set<string>();
  for (const candidate of workers) {
    if (candidate.child_session_id?.trim() === childId) {
      jobIds.add(candidate.id.trim());
    }
  }
  if (jobIds.size === 0) return undefined;
  let assistantId: string | undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const dispatch = messages[i]?.tool_result?.dispatch;
    if (!dispatch) continue;
    if (
      dispatch.child_session_id?.trim() === childId ||
      jobIds.has(dispatch.worker_id.trim())
    ) {
      assistantId = messages[i]?.tool_result?.assistant_message_id?.trim();
      if (assistantId) break;
    }
  }
  return assistantId
    ? messages.find((msg) => msg.id === assistantId)
    : undefined;
}

function latestAssistantAnchor(
  messages: readonly Message[],
): Message | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (msg?.role === "assistant") return msg;
  }
  return undefined;
}

function pendingCheckpointItems(
  messages: readonly Message[],
  workers: readonly WorkerTask[],
  checkpoints: readonly PendingCheckpoint[],
): RawTranscriptItem[] {
  const items: RawTranscriptItem[] = [];
  for (const checkpoint of checkpoints) {
    const toolCallId = checkpointToolCallId(checkpoint);
    let anchor =
      parentAnchorForChildCheckpoint(messages, workers, checkpoint) ??
      (toolCallId
        ? assistantAnchorForToolCall(messages, toolCallId)
        : undefined);
    if (!anchor) {
      anchor = latestAssistantAnchor(messages);
    }
    if (!anchor && messages.length > 0) {
      anchor = messages[messages.length - 1];
    }
    if (!anchor) continue;
    const index = toolCallId
      ? (anchor.tool_calls ?? []).findIndex((c) => c.id?.trim() === toolCallId)
      : -1;
    const call = anchor.tool_calls?.[index];
    items.push({
      kind: "checkpoint",
      key: `checkpoint:${checkpoint.checkpointId}`,
      parentMessageId: anchor.id,
      ...(call?.name ? { call: { tool: call.name, index } } : {}),
      meta: checkpointMetaFromPending(checkpoint, call),
    });
  }
  return items;
}

function injectCheckpointItemsIntoSpanBlocks(
  blocks: readonly ChatSpanBlock[],
  messages: readonly Message[],
  workers: readonly WorkerTask[],
  checkpoints: readonly PendingCheckpoint[],
  verboseMode: boolean,
): ChatSpanBlock[] {
  if (blocks.length === 0) return [...blocks];
  const existingKeys = new Set(
    blocks.flatMap((block) => block.items.map((item) => item.key)),
  );
  const additionsByRun = new Map<string, RawTranscriptItem[]>();
  const unassignedAdditions: RawTranscriptItem[] = [];
  const candidates = pendingCheckpointItems(messages, workers, checkpoints);
  const blockRunIds = new Set(blocks.map((b) => b.runId));

  for (const item of candidates) {
    if (existingKeys.has(item.key) || item.kind !== "checkpoint") continue;
    const anchor = messages.find((msg) => msg.id === item.parentMessageId);
    const runId = anchor?.workflow_run_id?.trim() ?? "";
    if (blockRunIds.has(runId)) {
      const additions = additionsByRun.get(runId) ?? [];
      additions.push(item);
      additionsByRun.set(runId, additions);
    } else {
      unassignedAdditions.push(item);
    }
    existingKeys.add(item.key);
  }

  if (additionsByRun.size === 0 && unassignedAdditions.length === 0) {
    return [...blocks];
  }

  return blocks.map((block, index) => {
    const runAdditions = additionsByRun.get(block.runId) ?? [];
    const isLastBlock = index === blocks.length - 1;
    const additions = isLastBlock
      ? [...runAdditions, ...unassignedAdditions]
      : runAdditions;
    if (additions.length === 0) return block;
    return {
      ...block,
      items: sortTranscriptItemsByOrd([...block.items, ...additions], messages, { verboseMode, layout: "chat" }),
    };
  });
}

/** Builds spans from row run ids without waiting for run hydration. */
export function buildChatTranscriptBlocks(
  messages: readonly Message[],
  runs: readonly WorkflowRun[],
  activeRun: WorkflowRun | undefined,
  options?: ChatSpanBuildOptions,
): ChatSpanBlock[] {
  if (messages.length === 0) return [];
  const runMap = runsByIdWithLeaf(
    runsByIdMap(runs),
    resolveLeafRun(activeRun, runs),
  );
  let blocks = buildSpanBlocks(
    messages,
    runMap,
    options,
  );
  if (options?.pendingCheckpoints?.length) {
    blocks = injectCheckpointItemsIntoSpanBlocks(
      blocks,
      messages,
      options.workers ?? [],
      options.pendingCheckpoints ?? [],
      options.verboseMode ?? false,
    );
  }
  // Empty buckets have no span chrome.
  return blocks.filter((block) => block.items.length > 0);
}

export function scrollTargetForRun(run: WorkflowRun): string | undefined {
  return run.start_message_id ?? undefined;
}
