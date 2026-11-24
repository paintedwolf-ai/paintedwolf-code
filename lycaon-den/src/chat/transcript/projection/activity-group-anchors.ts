import { activitySpanEntryKey, activitySpanToolParts } from "../../tool/activity-span-model.ts";
import { isTranscriptDisclosureOpen, isTranscriptDisclosureShell } from "../presentation/transcript-disclosure-shell.ts";
import type { ActivityGroupItem, TranscriptItem } from "./transcript-item-model.ts";

export const ACTIVITY_GROUP_KEY_PREFIX = "activity-group:";

export function activityGroupMembers(item: ActivityGroupItem): string[] {
  const members: string[] = [];
  const seen = new Set<string>();
  const add = (raw: string | undefined) => {
    const key = raw?.trim();
    if (key && !seen.has(key)) {
      seen.add(key);
      members.push(key);
    }
  };
  for (const entry of item.entries) {
    add(activitySpanEntryKey(entry));
    if (entry.kind === "tool") add(entry.part.messageId);
  }
  for (const approval of item.approvals) add(approval.key);
  for (const fold of item.folds) {
    for (const step of fold.steps) {
      add(step.key);
      add(step.messageId);
    }
  }
  return members;
}

export function activityGroupContainsMessage(item: ActivityGroupItem, id: string): boolean {
  return (
    item.anchorMessageId === id ||
    activitySpanToolParts(item.entries).some(
      (part) => part.messageId === id || part.assistantMessageId === id,
    ) ||
    item.entries.some((entry) => entry.kind === "index_warming" && entry.key === id) ||
    item.approvals.some(
      (approval) => approval.parentMessageId === id || approval.call?.resultMessageId === id,
    ) ||
    item.folds.some((fold) => fold.steps.some((step) => step.messageId === id))
  );
}

export function measureActivityGroupMemberOffsetPx(
  root: ParentNode | null | undefined,
  groupKey: string,
  memberKey: string,
  activitiesOpen: boolean,
  wasGroup: boolean,
  includeRowSeam: boolean,
): number | undefined {
  if (!root) return undefined;
  let groupRow: HTMLElement | null = null;
  for (const el of root.querySelectorAll<HTMLElement>("[data-msg-id]")) {
    if (el.dataset.msgId === groupKey) {
      groupRow = el;
      break;
    }
  }
  const scope = groupRow ?? root;
  const groupCard = scope.querySelector<HTMLElement>('[data-testid="activity-group-card"]') ?? groupRow;
  const seamPx = includeRowSeam && groupCard && groupRow && groupCard !== groupRow
    ? Math.max(0, groupCard.getBoundingClientRect().top - groupRow.getBoundingClientRect().top)
    : 0;

  let targetSection: HTMLElement | null = null;
  let entryDelta = 0;

  for (const el of scope.querySelectorAll<HTMLElement>("[data-activity-entry-key]")) {
    if (el.dataset.activityEntryKey === memberKey) {
      targetSection =
        el.closest<HTMLElement>("[data-activity-group-section]") ??
        el.closest<HTMLElement>(".den-activity-span");
      if (activitiesOpen) {
        const firstEntry = (targetSection ?? scope).querySelector<HTMLElement>("[data-activity-entry-key]");
        if (firstEntry && firstEntry !== el) {
          const rect = el.getBoundingClientRect().top - firstEntry.getBoundingClientRect().top;
          entryDelta = rect > 0 ? rect : Math.max(0, el.offsetTop - firstEntry.offsetTop);
        } else if (!wasGroup && groupRow) {
          const rect = el.getBoundingClientRect().top - groupRow.getBoundingClientRect().top;
          return rect > 0 ? rect : Math.max(0, el.offsetTop);
        }
      }
      break;
    }
  }

  if (!targetSection) {
    for (const el of scope.querySelectorAll<HTMLElement>("[data-checkpoint-id]")) {
      if (`checkpoint:${el.dataset.checkpointId}` === memberKey || el.dataset.checkpointId === memberKey) {
        targetSection = el.closest<HTMLElement>("[data-activity-group-section]") ?? el;
        break;
      }
    }
  }

  if (targetSection && groupCard) {
    const sectionRect = targetSection.getBoundingClientRect().top - groupCard.getBoundingClientRect().top;
    const sectionOffset = sectionRect > 0 ? sectionRect : Math.max(0, targetSection.offsetTop);
    const total = sectionOffset + entryDelta + seamPx;
    return total > 0 ? total : 0;
  }
  return entryDelta > 0 ? entryDelta + seamPx : undefined;
}

function matchesPreferredToolPath(card: HTMLElement, preferredPath: string): boolean {
  const normalized = preferredPath.trim();
  if (!normalized) return false;
  const mutatePath = card.dataset.activityMutatePath?.trim();
  if (
    mutatePath &&
    (mutatePath === normalized ||
      normalized.endsWith(`/${mutatePath}`) ||
      mutatePath.endsWith(`/${normalized}`))
  ) {
    return true;
  }
  const base = normalized.split("/").pop() ?? normalized;
  const text = card.textContent ?? "";
  return base.length > 0 && text.includes(base);
}

export function transcriptToolTarget(
  root: ParentNode,
  toolCallId: string,
  preferredPath?: string,
): HTMLElement | null {
  let firstCard: HTMLElement | null = null;
  let fallback: HTMLElement | null = null;
  for (const element of root.querySelectorAll<HTMLElement>("[data-tool-call-id]")) {
    if (element.dataset.toolCallId !== toolCallId) continue;
    const card = element.matches('.den-tool-part, [data-testid="task-card"]')
      ? element
      : element.closest<HTMLElement>('.den-tool-part, [data-testid="task-card"]');
    if (card) {
      if (!preferredPath) return card;
      if (matchesPreferredToolPath(card, preferredPath)) return card;
      firstCard ??= card;
      continue;
    }
    fallback ??= element;
  }
  if (firstCard) return firstCard;
  if (fallback) return fallback;
  for (const row of root.querySelectorAll<HTMLElement>("[data-tool-call-ids]")) {
    if (!row.dataset.toolCallIds?.split(" ").includes(toolCallId)) continue;
    if (isTranscriptDisclosureShell(row)) return row;
    for (const inner of row.querySelectorAll<HTMLElement>("[data-tool-call-ids]")) {
      if (inner.dataset.toolCallIds?.split(" ").includes(toolCallId) && isTranscriptDisclosureShell(inner)) {
        return inner;
      }
    }
    for (const cp of row.querySelectorAll<HTMLElement>("[data-approval-tool-call-id]")) {
      if (cp.dataset.approvalToolCallId === toolCallId) return cp;
    }
    const activitiesShell = row.querySelector<HTMLElement>(
      '[data-activity-group-section="activities"] [data-transcript-disclosure], .den-activity-span.den-transcript-disclosure-card',
    );
    if (activitiesShell) return activitiesShell;
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
    if (
      isTranscriptDisclosureShell(parent) &&
      (!isTranscriptDisclosureOpen(parent) || parent.hasAttribute("data-closing") || parent.dataset.animating === "true")
    ) {
      target = parent;
    }
  }
  return target;
}

/** Tool calls whose primary activity or approval row lives in this transcript item. */
export function transcriptItemActionToolCallIds(item: TranscriptItem): string | undefined {
  if (item.kind === "tool") return item.part.toolCallId;
  if (item.kind === "worker_group") return item.parts.map((part) => part.toolCallId).join(" ");
  if (item.kind === "activity_span") {
    return activitySpanToolParts(item.entries).map((part) => part.toolCallId).join(" ");
  }
  if (item.kind === "activity_group") {
    const ids = new Set<string>();
    for (const part of activitySpanToolParts(item.entries)) {
      if (part.toolCallId) ids.add(part.toolCallId);
    }
    for (const approval of item.approvals) {
      const id = approval.toolCallId ?? approval.call?.toolCallId;
      if (id) ids.add(id);
    }
    return ids.size > 0 ? [...ids].join(" ") : undefined;
  }
  return undefined;
}
