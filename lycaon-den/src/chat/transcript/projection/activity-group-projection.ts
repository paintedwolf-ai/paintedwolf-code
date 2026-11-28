import type { Message } from "../../../api/types.ts";
import { fileEditStepsFromPart, foldIntervalFileEdits, type FileEditStep } from "../../file-edit/file-edit-fold.ts";
import {
  activityCandidate, activityHeadline, activitySpanEntryKey, emitWorkerRun, runHasWork, type ActivityCandidate,
} from "../../tool/activity-span-model.ts";
import { isTaskToolName, toolPartStableId } from "../../tool/tool-part-model.ts";
import { toolDrawsWhileRunning } from "../../tool/tool-transcript-visibility.ts";
import {
  TIER, UNORDERED, callOrd, compareTranscriptOrdSortKeys, transcriptItemOrdKey,
  type TranscriptOrdSortKey, type TranscriptOrder,
} from "./transcript-item-order.ts";
import type {
  ActivityGroupApproval, ActivityGroupItem, ActivityGroupSection, DisplayTranscriptItem, RawTranscriptItem,
} from "./transcript-item-model.ts";

type ActivityGroupProjectionOptions = {
  verboseMode?: boolean;
  messages?: readonly Message[];
  order?: TranscriptOrder;
};

type TimedFileStep = { step: FileEditStep; key: TranscriptOrdSortKey };
type TimedOmittedFiles = { partId: string; messageId: string; count: number; key: TranscriptOrdSortKey };
type IntervalSlice = {
  candidates: ActivityCandidate[];
  approvals: ActivityGroupApproval[];
  fileSteps: TimedFileStep[];
  omitted: TimedOmittedFiles[];
};
type PlacedBoundary = { item: DisplayTranscriptItem; key: TranscriptOrdSortKey };

const emptySlice = (): IntervalSlice => ({ candidates: [], approvals: [], fileSteps: [], omitted: [] });

function syntheticOrder(items: readonly RawTranscriptItem[], verboseMode: boolean): TranscriptOrder {
  const ordById = new Map<string, number>();
  let nextOrd = 1;
  const assign = (...ids: (string | undefined)[]) => {
    for (const raw of ids) {
      const id = raw?.trim();
      if (id && !ordById.has(id)) ordById.set(id, nextOrd++);
    }
  };
  for (const item of items) {
    if (item.kind === "tool") assign(item.part.assistantMessageId, item.part.messageId);
    else if (item.kind === "checkpoint") assign(item.parentMessageId, item.call?.resultMessageId);
    else if (item.kind === "turn_load") assign(item.row.anchorMessageId);
    else if (item.kind === "worker_file_edit") assign(item.anchorMessageId);
    else assign(item.key);
  }
  return {
    ordById,
    noteIds: new Set(),
    drawsWhileRunning: (tool) => toolDrawsWhileRunning(tool, { verboseMode, layout: "chat" }),
  };
}

function isRenderableProgressUpdate(item: Extract<RawTranscriptItem, { kind: "progress_update" }>): boolean {
  return item.initial || item.summary != null || item.changes.length > 0;
}

/** Skip ungrounded carrier prose when immediately followed at the same ord by its settled note. */
function coalesceCarrierNotes(items: readonly RawTranscriptItem[], order: TranscriptOrder): RawTranscriptItem[] {
  if (order.noteIds.size === 0) return [...items];
  return items.filter((current, i) => {
    const next = items[i + 1];
    return !(
      current.kind === "assistant" &&
      !current.grounding &&
      next?.kind === "assistant" &&
      order.noteIds.has(next.key) &&
      order.ordById.get(current.key) !== undefined &&
      order.ordById.get(current.key) === order.ordById.get(next.key)
    );
  });
}

function resolveApprovalToolCallId(
  item: Extract<RawTranscriptItem, { kind: "checkpoint" }>,
  messagesById: ReadonlyMap<string, Message>,
  candidates: readonly ActivityCandidate[],
): string | undefined {
  const direct = item.toolCallId?.trim() || item.call?.toolCallId?.trim();
  if (direct) return direct;
  const index = item.call?.index;
  if (index === undefined || index < 0) return undefined;
  const fromParent = messagesById.get(item.parentMessageId)?.tool_calls?.[index]?.id?.trim();
  if (fromParent) return fromParent;
  const match = candidates.find(
    (c) => c.entry.kind === "tool" && c.entry.part.assistantMessageId === item.parentMessageId && c.entry.batchOrder === index,
  );
  return match?.entry.kind === "tool" ? match.entry.part.toolCallId : undefined;
}

function workEntryOrdKey(candidate: ActivityCandidate, order: TranscriptOrder): TranscriptOrdSortKey {
  const { entry } = candidate;
  return entry.kind === "tool"
    ? { ord: callOrd(entry.part, order), tier: TIER.call, index: 2 * (entry.batchOrder ?? 0) }
    : { ord: order.ordById.get(entry.key) ?? UNORDERED, tier: TIER.row, index: 0 };
}

function approvalFirstVisibilityKey(
  approval: ActivityGroupApproval,
  candidates: readonly ActivityCandidate[],
  order: TranscriptOrder,
): TranscriptOrdSortKey {
  const callIndex = approval.call?.index ?? 0;
  const parentOrd = order.ordById.get(approval.parentMessageId);
  const resultId = approval.call?.resultMessageId;
  const atParent = parentOrd !== undefined || !resultId;
  let ord = atParent ? (parentOrd ?? UNORDERED) : (order.ordById.get(resultId) ?? UNORDERED);
  if (ord !== UNORDERED && callIndex > 0) {
    for (const { entry } of candidates) {
      if (entry.kind === "tool" && entry.part.assistantMessageId === approval.parentMessageId && (entry.batchOrder ?? 0) < callIndex) {
        const sibOrd = entry.part.messageId !== entry.part.assistantMessageId ? order.ordById.get(entry.part.messageId) : undefined;
        if (sibOrd !== undefined && sibOrd > ord) ord = sibOrd;
      }
    }
  }
  return { ord, tier: TIER.call, index: atParent ? 2 * callIndex + 1 : 2 * callIndex - 1 };
}

function minOrdKey(keys: readonly TranscriptOrdSortKey[]): TranscriptOrdSortKey | undefined {
  let best: TranscriptOrdSortKey | undefined;
  for (const key of keys) if (!best || compareTranscriptOrdSortKeys(key, best) < 0) best = key;
  return best;
}

function resolveGroupLeadKey(
  workCandidates: readonly ActivityCandidate[],
  allCandidates: readonly ActivityCandidate[],
  approvals: readonly ActivityGroupApproval[],
  fileSteps: readonly TimedFileStep[],
  omitted: readonly TimedOmittedFiles[],
  order: TranscriptOrder,
): string {
  const leads: { id: string; key: TranscriptOrdSortKey }[] = [
    ...workCandidates.map((c) => ({ id: activitySpanEntryKey(c.entry), key: workEntryOrdKey(c, order) })),
    ...approvals.map((a) => ({
      id: a.parentMessageId && a.toolCallId ? toolPartStableId(a.parentMessageId, { id: a.toolCallId }) : a.key,
      key: approvalFirstVisibilityKey(a, allCandidates, order),
    })),
    ...fileSteps.map(({ step, key }) => ({ id: step.key, key })),
    ...omitted.map((entry) => ({ id: entry.partId, key: entry.key })),
  ];
  leads.sort((a, b) => compareTranscriptOrdSortKeys(a.key, b.key));
  return leads[0]?.id ?? "empty";
}

function resolveAnchorMessageId(
  entries: readonly ActivityCandidate[],
  approvals: readonly ActivityGroupApproval[],
  fileSteps: readonly TimedFileStep[],
  omitted: readonly TimedOmittedFiles[],
  order: TranscriptOrder,
  fallbackKey: string,
): string {
  const ids = [
    ...entries.flatMap(({ entry }) =>
      entry.kind === "tool" ? [entry.part.assistantMessageId, entry.part.messageId] : entry.kind === "index_warming" ? [entry.key] : [],
    ),
    ...approvals.flatMap((a) => [a.parentMessageId, a.call?.resultMessageId]),
    ...fileSteps.map(({ step }) => step.messageId),
    ...omitted.map((item) => item.messageId),
  ];
  let bestId: string | undefined;
  let bestOrd = UNORDERED;
  for (const raw of ids) {
    const id = raw?.trim();
    if (!id) continue;
    bestId ??= id;
    const ord = order.ordById.get(id);
    if (ord !== undefined && ord > 0 && ord < bestOrd) {
      bestOrd = ord;
      bestId = id;
    }
  }
  return bestId ?? fallbackKey;
}

function buildGroupFromSlice(
  slice: IntervalSlice,
  messagesById: ReadonlyMap<string, Message>,
  order: TranscriptOrder,
): ActivityGroupItem | null {
  const hasWork = runHasWork(slice.candidates);
  const workCandidates = hasWork ? slice.candidates.filter((c) => c.entry.kind !== "turn_load") : [];
  const approvals: ActivityGroupApproval[] = slice.approvals.map((approval) => {
    const toolCallId = resolveApprovalToolCallId(approval, messagesById, slice.candidates);
    return toolCallId && toolCallId !== approval.toolCallId ? { ...approval, toolCallId } : approval;
  });
  const filesOmitted = slice.omitted.reduce((sum, entry) => sum + entry.count, 0);
  if (!hasWork && approvals.length === 0 && slice.fileSteps.length === 0 && filesOmitted === 0) return null;

  const groupKey = `activity-group:${resolveGroupLeadKey(workCandidates, slice.candidates, approvals, slice.fileSteps, slice.omitted, order)}`;
  const folds = slice.fileSteps.length > 0 ? foldIntervalFileEdits(groupKey, slice.fileSteps.map((s) => s.step)) : [];

  const rankedSections: { section: ActivityGroupSection; key: TranscriptOrdSortKey }[] = [];
  const actKey = hasWork ? minOrdKey(workCandidates.map((c) => workEntryOrdKey(c, order))) : undefined;
  if (actKey) rankedSections.push({ section: "activities", key: actKey });
  const appKey = approvals.length > 0 ? minOrdKey(approvals.map((a) => approvalFirstVisibilityKey(a, slice.candidates, order))) : undefined;
  if (appKey) rankedSections.push({ section: "approvals", key: appKey });
  const fileKey = folds.length > 0 || filesOmitted > 0
    ? minOrdKey([...slice.fileSteps.map((s) => s.key), ...slice.omitted.map((o) => o.key)])
    : undefined;
  if (fileKey) rankedSections.push({ section: "files", key: fileKey });
  rankedSections.sort((a, b) => compareTranscriptOrdSortKeys(a.key, b.key));

  const activeCandidates = hasWork ? slice.candidates : [];
  return {
    kind: "activity_group",
    key: groupKey,
    sectionOrder: rankedSections.map((entry) => entry.section),
    approvals,
    folds,
    ...(filesOmitted > 0 ? { filesOmitted } : {}),
    headline: hasWork ? activityHeadline(activeCandidates) : "activity",
    entries: activeCandidates.map((candidate) => candidate.entry),
    anchorMessageId: resolveAnchorMessageId(activeCandidates, approvals, slice.fileSteps, slice.omitted, order, groupKey),
  };
}

/** Project canonically ordered chat items into interval-scoped activity groups. */
export function projectActivityGroups(
  items: readonly RawTranscriptItem[],
  opts?: ActivityGroupProjectionOptions,
): DisplayTranscriptItem[] {
  const verboseMode = opts?.verboseMode ?? false;
  const order = opts?.order ?? syntheticOrder(items, verboseMode);
  const deduped = coalesceCarrierNotes(items, order);
  const messagesById = new Map<string, Message>();
  for (const msg of opts?.messages ?? []) {
    if (!messagesById.has(msg.id)) messagesById.set(msg.id, msg);
  }

  const slices: IntervalSlice[] = [emptySlice()];
  const boundaries: PlacedBoundary[] = [];
  const deferredFileTools: { sliceIndex: number; item: Extract<RawTranscriptItem, { kind: "tool" }> }[] = [];
  let workerRun: Extract<RawTranscriptItem, { kind: "tool" }>[] = [];
  let stepSequence = 0;

  const pushBoundary = (item: DisplayTranscriptItem, key: TranscriptOrdSortKey) => {
    boundaries.push({ item, key });
    slices.push(emptySlice());
  };
  const flushWorkerRun = () => {
    if (workerRun.length === 0) return;
    const key = transcriptItemOrdKey(workerRun[0]!, order);
    for (const group of emitWorkerRun(workerRun)) pushBoundary(group, key);
    workerRun = [];
  };

  for (const item of deduped) {
    if (item.kind === "progress_update" && !isRenderableProgressUpdate(item)) continue;
    if (item.kind === "checkpoint" && item.childSession) continue;
    if (item.kind === "tool" && isTaskToolName(item.part.tool)) {
      workerRun.push(item);
      continue;
    }
    flushWorkerRun();
    if (item.kind === "tool" && item.part.tool === "instruct_worker") {
      pushBoundary(item, transcriptItemOrdKey(item, order));
      continue;
    }
    if (item.kind === "checkpoint") {
      const { kind, key, parentMessageId, call, meta } = item;
      slices[slices.length - 1]!.approvals.push({ kind, key, parentMessageId, call, toolCallId: item.toolCallId ?? call?.toolCallId, meta });
      continue;
    }
    if (item.kind === "tool" || item.kind === "index_warming" || item.kind === "turn_load") {
      const sliceIndex = slices.length - 1;
      slices[sliceIndex]!.candidates.push(activityCandidate(item, { layout: "chat", verboseMode }));
      if (item.kind === "tool" && ((item.part.fileEdits?.length ?? 0) > 0 || (item.part.fileEditsOmitted ?? 0) > 0)) {
        deferredFileTools.push({ sliceIndex, item });
      }
      continue;
    }
    pushBoundary(item, transcriptItemOrdKey(item, order));
  }
  flushWorkerRun();

  for (const { sliceIndex, item: { part } } of deferredFileTools) {
    const resultOrd = order.ordById.get(part.messageId) ?? UNORDERED;
    const drawnInFlight = Boolean(part.shownInFlight || order.drawsWhileRunning(part.tool));
    let targetSlice = sliceIndex;
    if (drawnInFlight && resultOrd !== UNORDERED && part.messageId !== part.assistantMessageId) {
      const diffKey: TranscriptOrdSortKey = { ord: resultOrd, tier: TIER.diff, index: 0 };
      while (
        targetSlice < boundaries.length &&
        boundaries[targetSlice]!.key.ord !== UNORDERED &&
        compareTranscriptOrdSortKeys(boundaries[targetSlice]!.key, diffKey) < 0
      ) {
        targetSlice += 1;
      }
    }
    const dest = slices[targetSlice]!;
    for (const step of fileEditStepsFromPart(part)) {
      dest.fileSteps.push({ step, key: { ord: resultOrd, tier: TIER.diff, index: stepSequence++ } });
    }
    if ((part.fileEditsOmitted ?? 0) > 0) {
      dest.omitted.push({ partId: part.id, messageId: part.messageId, count: part.fileEditsOmitted!, key: { ord: resultOrd, tier: TIER.diff, index: stepSequence++ } });
    }
  }

  const out: DisplayTranscriptItem[] = [];
  for (let i = 0; i < slices.length; i++) {
    const group = buildGroupFromSlice(slices[i]!, messagesById, order);
    if (group) out.push(group);
    if (i < boundaries.length) out.push(boundaries[i]!.item);
  }
  return out;
}
