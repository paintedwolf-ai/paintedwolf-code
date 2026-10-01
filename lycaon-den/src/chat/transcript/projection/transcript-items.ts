import type { Message } from "../../../api/types.ts";
import { enrichCheckpointDecision } from "../../checkpoint/approval-display.ts";
import { blueprintCardViewFromMeta } from "../../../blueprint/blueprint-card-view.ts";
import { foldFileEdits } from "../../file-edit/file-edit-fold.ts";
import { omitIndexWarmingFromTranscript, projectActivitySpans } from "../../tool/activity-span-model.ts";
import { shouldShowIndexWarmingInTranscript } from "./index-warming-copy.ts";
import type { TranscriptLayout } from "../layout/transcript-layout.ts";
import { shouldOmitToolPartFromTranscript, toolPartFromCall, toolPartStableId } from "../../tool/tool-part-model.ts";
import { isCoordinatorDraftWire } from "./draft-model.ts";
import { isDenTranscriptMessage, isInternalTranscriptUserMessage, isWorkflowBoundaryMessage } from "./message-transcript.ts";
import { workflowBoundaryLabel } from "../../workflow/workflow-boundary-label.ts";
import { coordinatorSlotItem } from "./coordinator-slot.ts";
import { turnLoadRows } from "../../turnload/turn-load-rows.ts";
import { type TranscriptItem, type RawTranscriptItem, type DisplayTranscriptItem, type TranscriptBuildOptions } from "./transcript-item-model.ts";

function fallbackTranscriptItem(msg: Message): Extract<TranscriptItem, { kind: "fallback" }> {
  return {
    kind: "fallback",
    key: msg.id,
    role: msg.role,
    messageKind: msg.kind?.trim() || undefined,
    text: msg.content,
  };
}

type TranscriptToolBuildOpts = {
  verboseMode: boolean;
  layout: TranscriptLayout;
};

type ToolMessageIndex = {
  messages: Map<string, Message>;
  results: Map<string, Set<string>>;
};

function indexToolMessages(messages: readonly Message[]): ToolMessageIndex {
  const index: ToolMessageIndex = { messages: new Map(), results: new Map() };
  for (const message of messages) {
    if (!index.messages.has(message.id)) index.messages.set(message.id, message);
    const result = message.role === "tool" ? message.tool_result : undefined;
    const parent = result?.assistant_message_id?.trim();
    const call = result?.tool_call_id?.trim();
    if (!parent || !call) continue;
    let calls = index.results.get(parent);
    if (!calls) index.results.set(parent, calls = new Set());
    calls.add(call);
  }
  return index;
}

function appendRunningToolsForAssistant(
  index: ToolMessageIndex,
  msg: Message,
  items: RawTranscriptItem[],
  renderedToolKeys: Set<string>,
  opts: TranscriptToolBuildOpts,
): void {
  if (!msg.tool_calls?.length) return;
  // Withdrawn calls have no result.
  if (msg.draft_status === "withdrawn") return;
  for (let callIndex = 0; callIndex < msg.tool_calls.length; callIndex++) {
    const call = msg.tool_calls[callIndex]!;
    const wireId = call.id?.trim();
    if (!wireId) continue;
    const stableId = toolPartStableId(msg.id, call);
    if (renderedToolKeys.has(stableId)) continue;
    const hasResult = index.results.get(msg.id.trim())?.has(wireId);
    if (hasResult) continue;
    if (!call.name?.trim()) continue;
    if (shouldOmitToolPartFromTranscript(undefined, call.name, opts)) {
      continue;
    }
    const part = toolPartFromCall(
      call,
      undefined,
      msg.id,
      msg.workflow_run_id,
      { message: msg, index: callIndex },
    );
    renderedToolKeys.add(stableId);
    items.push({ kind: "tool", key: part.id, part, batchOrder: callIndex });
  }
}

function appendToolResultRow(
  index: ToolMessageIndex,
  msg: Message,
  items: RawTranscriptItem[],
  renderedToolKeys: Set<string>,
  opts: TranscriptToolBuildOpts,
): void {
  const parent = index.messages.get(msg.tool_result?.assistant_message_id?.trim() ?? "");
  const assistant = parent?.role === "assistant" ? parent : undefined;
  // The assistant row renders calls without results.
  const toolCallId = msg.tool_result?.tool_call_id?.trim();
  const toolName = msg.tool_result?.tool?.trim();
  if (!toolCallId || !toolName) return;
  const assistantMessageId =
    assistant?.id?.trim() || msg.tool_result?.assistant_message_id?.trim();
  if (!assistantMessageId) return;
  const callIndex =
    assistant?.tool_calls?.findIndex((row) => row.id?.trim() === toolCallId) ?? -1;
  const call = {
    id: toolCallId,
    name: toolName,
    args: msg.tool_result?.tool_args ?? {},
  };
  const stableId = toolPartStableId(assistantMessageId, call);
  if (renderedToolKeys.has(stableId)) return;
  const decision = msg.tool_result?.checkpoint_decision;
  if (!shouldOmitToolPartFromTranscript(msg, call.name ?? "", opts)) {
    const part = toolPartFromCall(
      call,
      msg,
      assistantMessageId,
      assistant?.workflow_run_id ?? msg.workflow_run_id,
      assistant && callIndex >= 0 ? { message: assistant, index: callIndex } : undefined,
    );
    renderedToolKeys.add(stableId);
    items.push({
      kind: "tool",
      key: part.id,
      part,
      batchOrder: callIndex >= 0 ? callIndex : undefined,
    });
  }
  if (decision) {
    items.push({
      kind: "checkpoint",
      key: `checkpoint:${decision.checkpoint_id}`,
      parentMessageId: assistantMessageId,
      meta: enrichCheckpointDecision(decision, call, msg.tool_result),
    });
  }
}

function applyVerboseTranscriptVisibility(
  items: readonly RawTranscriptItem[],
  opts: TranscriptBuildOptions,
): RawTranscriptItem[] {
  let visible = [...items];
  if (!shouldShowIndexWarmingInTranscript(opts.verboseMode)) {
    visible = omitIndexWarmingFromTranscript(visible);
  }
  if (opts.layout !== "worker" && opts.verboseMode !== true) {
    visible = visible.filter((item) => item.kind !== "draft");
  }
  return visible;
}

/** Apply visibility, then project raw activity into spans. */

export function finalizeTranscriptItems(
  items: readonly RawTranscriptItem[],
  opts?: TranscriptBuildOptions,
): DisplayTranscriptItem[] {
  const visible = applyVerboseTranscriptVisibility(items, opts ?? {});
  return projectActivitySpans(visible, { layout: opts?.layout });
}

/** Pair tool results with assistant calls by durable parent and call identity. */

export function messagesToTranscriptItems(
  messages: readonly Message[],
  opts?: TranscriptBuildOptions,
): RawTranscriptItem[] {
  const verboseMode = opts?.verboseMode ?? false;
  const layout = opts?.layout ?? "chat";
  const items: RawTranscriptItem[] = [];
  const renderedToolKeys = new Set<string>();
  const toolOpts: TranscriptToolBuildOpts = {
    verboseMode,
    layout,
  };
  const orderedMessages = [...messages].sort(
    (a, b) =>
      (a.ord ?? Number.MAX_SAFE_INTEGER) -
        (b.ord ?? Number.MAX_SAFE_INTEGER) ||
      a.id.localeCompare(b.id),
  );

  const toolIndex = indexToolMessages(orderedMessages);
  for (let i = 0; i < orderedMessages.length; i++) {
    const msg = orderedMessages[i]!;

    if (msg.role === "tool") {
      const promotion = msg.tool_result?.promotion_previews;
      if (promotion?.length) {
        items.push({
          kind: "worker_file_edit",
          key: `worker-file-edit:${msg.id}`,
          anchorMessageId: msg.id,
          folds: foldFileEdits(promotion.map((snapshot, index) => ({
            key: `${msg.id}:${index}`,
            toolCallId: msg.tool_result?.tool_call_id,
            messageId: msg.id,
            snapshot,
            tool: "promote_overlay",
            ts: msg.created_at,
          }))),
          ts: msg.created_at,
        });
      }
      appendToolResultRow(
        toolIndex,
        msg,
        items,
        renderedToolKeys,
        toolOpts,
      );
      continue;
    }

    // Boundaries precede draft classification and retain transcript visibility.
    if (isWorkflowBoundaryMessage(msg)) {
      if (!isDenTranscriptMessage(msg)) continue;
      items.push({
        kind: "workflow_boundary",
        key: msg.id,
        label: workflowBoundaryLabel(msg.workflow_boundary),
      });
      continue;
    }

    const internalCoordinatorRow =
      layout === "chat" &&
      msg.role === "assistant" &&
      msg.visibility === "internal" &&
      (isCoordinatorDraftWire(msg) || (msg.tool_calls?.length ?? 0) > 0);

    if (!isDenTranscriptMessage(msg) && !internalCoordinatorRow) {
      continue;
    }

    if (msg.kind === "blueprint" || msg.blueprint) {
      const meta = msg.blueprint;
      if (meta) {
        items.push({
          kind: "blueprint_card",
          key: msg.id,
          meta,
          content: msg.content,
          view: blueprintCardViewFromMeta(meta),
        });
      } else {
        items.push(fallbackTranscriptItem(msg));
      }
      continue;
    }

    if (msg.kind === "progress_complete" || msg.progress_complete) {
      items.push({
        kind: "progress_complete",
        key: msg.id,
        steps: msg.progress_complete?.steps ?? [],
      });
      continue;
    }

    if (msg.kind === "progress_update" || msg.progress_update) {
      items.push({
        kind: "progress_update",
        key: msg.id,
        initial: msg.progress_update?.initial ?? false,
        steps: msg.progress_update?.steps ?? [],
        changes: msg.progress_update?.changes ?? [],
        summary: msg.progress_update?.summary,
      });
      continue;
    }

    if (msg.kind === "workflow_feedback") {
      const meta = msg.workflow_feedback;
      const prompt = (meta?.prompt ?? msg.content).trim();
      if (prompt) {
        items.push({
          kind: "workflow_feedback",
          key: msg.id,
          meta: meta ?? { phase_id: "", prompt, response_type: "text" },
          runId: msg.workflow_run_id,
        });
      } else {
        items.push(fallbackTranscriptItem(msg));
      }
      continue;
    }

    if (msg.kind === "workflow_explain" || msg.workflow_explain) {
      const meta = msg.workflow_explain;
      if (meta) {
        items.push({ kind: "workflow_explain", key: msg.id, meta, runId: msg.workflow_run_id });
      } else {
        items.push(fallbackTranscriptItem(msg));
      }
      continue;
    }

    // Index warming renders only through activity spans.
    if (msg.kind === "index_warming") {
      if (!msg.index_warming) continue;
      if (
        !shouldShowIndexWarmingInTranscript(verboseMode)
      ) {
        continue;
      }
      items.push({
        kind: "index_warming",
        key: msg.id,
        meta: msg.index_warming,
        workflowRunId: msg.workflow_run_id,
      });
      continue;
    }

    if (
      msg.role === "user" &&
      (msg.content.trim() || (msg.artifact_ids?.length ?? 0) > 0)
    ) {
      if (isInternalTranscriptUserMessage(msg)) continue;
      const artifactIds = (msg.artifact_ids ?? []).map((id) => id.trim()).filter(Boolean);
      items.push({
        kind: "user",
        key: msg.id,
        text: msg.content,
        artifactIds: artifactIds.length > 0 ? artifactIds : undefined,
      });
      continue;
    }

    if (msg.role === "assistant") {
      const slotItem = coordinatorSlotItem(msg, {
        layout,
      });
      if (slotItem) {
        items.push(slotItem);
        appendRunningToolsForAssistant(
          toolIndex,
          msg,
          items,
          renderedToolKeys,
          toolOpts,
        );
        continue;
      }
      if (msg.content.trim()) {
        items.push({
          kind: "assistant",
          key: msg.id,
          text: msg.content,
          grounding: msg.grounding,
          navigationRefs: msg.navigation_refs,
        });
      }
      appendRunningToolsForAssistant(
        toolIndex,
        msg,
        items,
        renderedToolKeys,
        toolOpts,
      );
      continue;
    }

    // Preserve visible rows without a specialized card.
    items.push(fallbackTranscriptItem(msg));
  }

  // Decision rows anchor to the assistant row whose calls they decided for.
  for (const row of turnLoadRows(orderedMessages, opts?.turnLoads)) {
    items.push({ kind: "turn_load", key: row.key, row });
  }

  return items;
}

/** Maps message ids to creation order. */
