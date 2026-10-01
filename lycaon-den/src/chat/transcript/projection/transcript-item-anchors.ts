import { activitySpanToolParts } from "../../tool/activity-span-model.ts";
import type { TranscriptItem } from "./transcript-item-model.ts";

export function transcriptItemToolCallIds(
  item: TranscriptItem,
): string | undefined {
  if (item.kind === "tool") return item.part.toolCallId;
  if (item.kind === "worker_group") {
    return item.parts.map((part) => part.toolCallId).join(" ");
  }
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries)
      .map((part) => part.toolCallId)
      .join(" ");
  }
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return [...new Set(item.folds.flatMap((fold) =>
      fold.steps.flatMap((step) => step.toolCallId ? [step.toolCallId] : []),
    ))].join(" ") || undefined;
  }
  return undefined;
}

/** Checks direct and grouped transcript rows. */
export function transcriptItemContainsMessage(
  item: TranscriptItem,
  messageId: string,
): boolean {
  const id = messageId.trim();
  if (!id || item.kind === "pending_user") return false;
  if (item.key === id) return true;
  if (item.kind === "file_edit" || item.kind === "worker_file_edit") {
    return item.anchorMessageId === id || item.folds.some((fold) =>
      fold.steps.some((step) => step.messageId === id),
    );
  }
  if (item.kind === "tool") return item.part.messageId === id;
  if (item.kind === "worker_group") {
    return item.parts.some((part) => part.messageId === id);
  }
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries).some((part) => part.messageId === id);
  }
  return false;
}

export function transcriptToolTarget(
  root: ParentNode,
  toolCallId: string,
): HTMLElement | null {
  let fallback: HTMLElement | null = null;
  for (const element of root.querySelectorAll<HTMLElement>(
    "[data-tool-call-id]",
  )) {
    if (element.dataset.toolCallId !== toolCallId) continue;
    const card = element.matches('.den-tool-part, [data-testid="task-card"]')
      ? element
      : element.closest<HTMLElement>(
          '.den-tool-part, [data-testid="task-card"]',
        );
    if (card) return card;
    fallback ??= element;
  }
  if (fallback) return fallback;
  for (const row of root.querySelectorAll<HTMLElement>("[data-tool-call-ids]")) {
    if (!row.dataset.toolCallIds?.split(" ").includes(toolCallId)) continue;
    return row.querySelector<HTMLElement>(".den-transcript-disclosure-card") ?? row;
  }
  return null;
}

/** The command itself, or the outermost disclosure currently hiding it. */
export function visibleTranscriptToolTarget(
  root: HTMLElement,
  toolCallId: string,
): HTMLElement | null {
  const command = transcriptToolTarget(root, toolCallId);
  if (!command) return null;
  let target = command;
  for (let parent = command.parentElement; parent && parent !== root; parent = parent.parentElement) {
    if (parent instanceof HTMLDetailsElement && (
      !parent.open || parent.hasAttribute("data-closing") || parent.dataset.animating === "true"
    )) target = parent;
  }
  return target;
}
