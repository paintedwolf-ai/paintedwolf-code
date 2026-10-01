import {
  SKILL_CHICKLET_NAME,
  isSkillToolPart,
} from "../skill/skill-card-model.ts";
import { fileEditFromPart } from "../file-edit/file-edit-model.ts";
import { foldFileEdits } from "../file-edit/file-edit-fold.ts";
import type { FileEditFold, FileEditStep } from "../file-edit/file-edit-fold.ts";
import { indexWarmingChickletTitle } from "../transcript/projection/index-warming-copy.ts";
import {
  isLoopbackTarget,
  isTaskToolName,
  toolPartDisplayStatus,
  toolPartSummaryTitle,
} from "./tool-part-model.ts";
import { ACTIVITY_ROLE_TIERS, TOOL_ACTIVITY_PRESENTATION } from "./tool-presentation.generated.ts";
import type { ToolActivityRole } from "./tool-presentation.generated.ts";
import type { ToolPartStatus, ToolPartView } from "./tool-part-model.ts";
import type { ActivitySpanEntry, DisplayTranscriptItem, RawTranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import type { TranscriptLayout } from "../transcript/layout/transcript-layout.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { LOCAL_AI_LABEL, turnLoadRowSummary } from "../turnload/turn-load-rows.ts";

const INDEX_WARMING_ACTIVITY = { headline: "researching", weight: 1, role: "research" as const } as const;
/** A decision row joins its span the way request_tools does: it never names the work. */
const TURN_LOAD_ACTIVITY = { weight: 0, role: "coordination" as const } as const;
const UNKNOWN_TOOL_WEIGHT = 1;
const WORKER_CONSECUTIVE_SCOPE = "worker:consecutive";

type ActivitySpanItem = Extract<DisplayTranscriptItem, { kind: "activity_span" }>;
type WorkerGroupItem = Extract<DisplayTranscriptItem, { kind: "worker_group" }>;

export type ActivityCandidate = {
  entry: ActivitySpanEntry;
  /** Host scope that permits adjacent calls to join. */
  scope: string;
  headline: string;
  weight: number;
  role?: ToolActivityRole;
};

type ActivitySpanProjectionOptions = {
  layout?: TranscriptLayout;
};

export function activitySpanToolParts(
  entries: readonly ActivitySpanEntry[],
): ToolPartView[] {
  const parts: ToolPartView[] = [];
  for (const entry of entries) {
    if (entry.kind === "tool") parts.push(entry.part);
  }
  return parts;
}

export function omitIndexWarmingFromTranscript(
  items: readonly RawTranscriptItem[],
): RawTranscriptItem[] {
  return items.filter((item) => item.kind !== "index_warming");
}

function entryCommandLabel(entry: ActivitySpanEntry): string {
  if (entry.kind === "index_warming") return "index warming";
  if (entry.kind === "turn_load") return LOCAL_AI_LABEL;
  if (isSkillToolPart(entry.part)) return SKILL_CHICKLET_NAME;
  return entry.part.tool.trim().toLowerCase() || "tool activity";
}

/** Adjacent calls join when the host scoped them together: one run, one worker loop, or one assistant row. */
function activityScope(
  fallback: string,
  workflowRunId: string | undefined,
  assistantMessageId: string,
  opts?: ActivitySpanProjectionOptions,
): string {
  if (workflowRunId) return `run:${workflowRunId}`;
  // One child session spans the worker's tool loop.
  if (opts?.layout === "worker") return WORKER_CONSECUTIVE_SCOPE;
  if (assistantMessageId) return `assistant:${assistantMessageId}`;
  return fallback;
}

function activityCandidate(
  item: Extract<RawTranscriptItem, { kind: "tool" | "index_warming" | "turn_load" }>,
  opts?: ActivitySpanProjectionOptions,
): ActivityCandidate {
  if (item.kind === "index_warming") {
    const scope = item.workflowRunId?.trim();
    return {
      entry: {
        kind: "index_warming",
        key: item.key,
        meta: item.meta,
        workflowRunId: item.workflowRunId,
      },
      scope: scope ? `run:${scope}` : `entry:${item.key}`,
      ...INDEX_WARMING_ACTIVITY,
    };
  }
  if (item.kind === "turn_load") {
    const entry: ActivitySpanEntry = { kind: "turn_load", key: item.key, row: item.row };
    return {
      entry,
      scope: activityScope(`entry:${item.key}`, item.row.workflowRunId?.trim(), item.row.assistantMessageId.trim(), opts),
      headline: entryCommandLabel(entry),
      ...TURN_LOAD_ACTIVITY,
    };
  }
  const entry: ActivitySpanEntry = {
    kind: "tool",
    part: item.part,
    batchOrder: item.batchOrder,
  };
  const workflowRunId = item.part.workflowRunId?.trim();
  const assistantMessageId = item.part.assistantMessageId.trim();
  const toolName = item.part.tool.trim().toLowerCase();
  const presentation = TOOL_ACTIVITY_PRESENTATION[toolName];
  const scope = activityScope(`entry:${item.part.id}`, workflowRunId, assistantMessageId, opts);

  let headline = presentation?.headline ?? entryCommandLabel(entry);
  const weight = presentation?.weight ?? UNKNOWN_TOOL_WEIGHT;
  const role = presentation?.role;

  if (toolName === "http_request" && isLoopbackTarget(item.part.args)) {
    headline = "testing endpoints";
  }

  return {
    entry,
    scope,
    headline,
    weight,
    role,
  };
}

function entryKey(entry: ActivitySpanEntry): string {
  return entry.kind === "tool" ? entry.part.id : entry.key;
}

/** Running wins; otherwise the newest settled tool determines the status dot. */
export function activitySpanStatus(
  entries: readonly ActivitySpanEntry[],
  sessionId?: string,
): ToolPartStatus {
  const statuses = activitySpanToolParts(entries).map((part) =>
    toolPartDisplayStatus(part, sessionId),
  );
  if (statuses.includes("running")) return "running";
  return statuses[statuses.length - 1] === "error" ? "error" : "completed";
}

/** Failures remain explicit even when later work recovers the status dot. */
export function activitySpanFailureCount(
  entries: readonly ActivitySpanEntry[],
  sessionId?: string,
): number {
  return activitySpanToolParts(entries).filter(
    (part) => toolPartDisplayStatus(part, sessionId) === "error",
  ).length;
}

export type ActivitySpanCompositionPart = {
  label: string;
  count: number;
};

/** Ordered tool-call composition for the expanded span. */
export function activitySpanComposition(
  entries: readonly ActivitySpanEntry[],
): ActivitySpanCompositionPart[] {
  const order: string[] = [];
  const counts = new Map<string, number>();
  for (const entry of entries) {
    const label = entryCommandLabel(entry);
    if (!counts.has(label)) {
      order.push(label);
      counts.set(label, 0);
    }
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return order.map((label) => ({ label, count: counts.get(label) ?? 0 }));
}

export function formatActivitySpanComposition(
  parts: readonly ActivitySpanCompositionPart[],
): string {
  return parts
    .map((part) => `${formatSentenceCase(part.label)} ×${part.count}`)
    .join(" · ");
}

/** Resolves the specificity tier for a candidate from its role. */
export function candidateTier(candidate: Pick<ActivityCandidate, "role">): number {
  return candidate.role ? ACTIVITY_ROLE_TIERS[candidate.role] : 0;
}

/** Detects whether an activity run represents a synthesized composite intent. */
export function detectCompositeHeadline(
  run: readonly ActivityCandidate[],
): string | null {
  if (run.length < 2) return null;

  let hasInterfaceTest = false;
  let hasLocalEndpoint = false;
  let hasAutomatedTest = false;
  let hasInspection = false;
  let hasCommand = false;

  for (const candidate of run) {
    if (candidate.role === "interface_test") hasInterfaceTest = true;
    if (candidate.headline === "testing endpoints") {
      hasLocalEndpoint = true;
    }
    if (candidate.role === "automated_test") hasAutomatedTest = true;
    if (candidate.role === "inspection") hasInspection = true;
    if (candidate.role === "command") hasCommand = true;
  }

  if (
    hasInterfaceTest &&
    (hasLocalEndpoint || hasAutomatedTest || hasCommand || hasInspection)
  ) {
    return "testing the application";
  }

  if (hasAutomatedTest && (hasLocalEndpoint || hasInterfaceTest)) {
    return "testing the application";
  }

  return null;
}

export const HYSTERESIS_MARGIN = 3;

/** Resolves the headline for an activity span run using composite intent and specificity tiers. */
export function activityHeadline(candidates: readonly ActivityCandidate[]): string {
  // Decision rows describe how the work was prepared, never what it was.
  const run = candidates.filter((candidate) => candidate.entry.kind !== "turn_load");
  if (run.length === 0) return "activity";

  const composite = detectCompositeHeadline(run);
  if (composite) {
    return composite;
  }

  const scores = new Map<string, number>();
  let winner = run[0]!.headline;
  let winnerScore = run[0]!.weight;
  let winnerTier = candidateTier(run[0]!);
  scores.set(winner, winnerScore);

  for (let i = 1; i < run.length; i++) {
    const candidate = run[i]!;
    const newScore = (scores.get(candidate.headline) ?? 0) + candidate.weight;
    scores.set(candidate.headline, newScore);

    if (candidate.headline === winner) {
      winnerScore = newScore;
      continue;
    }

    const tier = candidateTier(candidate);

    // Higher-tier winner does not downgrade.
    if (tier < winnerTier) {
      continue;
    }

    // Higher tier promotes immediately with positive weight.
    if (tier > winnerTier) {
      if (candidate.weight > 0) {
        winner = candidate.headline;
        winnerScore = newScore;
        winnerTier = tier;
      }
      continue;
    }

    // Lateral transitions require the hysteresis margin.
    if (newScore >= winnerScore + HYSTERESIS_MARGIN) {
      winner = candidate.headline;
      winnerScore = newScore;
      winnerTier = tier;
    }
  }

  return winner;
}

function emitActivitySpan(run: readonly ActivityCandidate[]): ActivitySpanItem {
  const entries = run.map((candidate) => candidate.entry);
  return {
    kind: "activity_span",
    key: entryKey(entries[0]!),
    label: activityHeadline(run),
    entries,
  };
}

/** A decision only shows beside the work it decided for; on its own it would be one more card. */
function runHasWork(run: readonly ActivityCandidate[]): boolean {
  return run.some((candidate) => candidate.entry.kind !== "turn_load");
}

function emitWorkerRun(
  run: readonly Extract<RawTranscriptItem, { kind: "tool" }>[],
): WorkerGroupItem[] {
  if (run.length === 0) return [];
  const first = run[0]!;
  const group: WorkerGroupItem = {
    kind: "worker_group",
    key: first.key,
    parts: run.map((item) => item.part),
  };
  return [group];
}

function candidateIsFileEdit(candidate: ActivityCandidate): boolean {
  return (
    candidate.entry.kind === "tool" &&
    fileEditFromPart(candidate.entry.part) != null
  );
}

/** Groups one turn's writes by path and first write. */
type SegmentFolds = {
  folds: FileEditFold[];
  /** First writes anchor each path's diff card. */
  anchorPartIds: Set<string>;
  anchorMessageIdByPartId: Map<string, string>;
};

/** Repeated writes share a card anchored at the first write. */
function collectSegmentFolds(
  items: readonly RawTranscriptItem[],
): SegmentFolds[] {
  const segments: SegmentFolds[] = [];
  let steps: FileEditStep[] = [];
  let anchorPartIds = new Set<string>();
  let anchorMessageIdByPartId = new Map<string, string>();
  let seenPaths = new Set<string>();

  const closeSegment = () => {
    segments.push({
      folds: foldFileEdits(steps),
      anchorPartIds,
      anchorMessageIdByPartId,
    });
    steps = [];
    anchorPartIds = new Set();
    anchorMessageIdByPartId = new Map();
    seenPaths = new Set();
  };

  for (const item of items) {
    if (item.kind === "user") {
      closeSegment();
      continue;
    }
    if (item.kind !== "tool") continue;
    const snapshot = fileEditFromPart(item.part);
    if (!snapshot) continue;
    steps.push({
      key: item.part.id,
      toolCallId: item.part.toolCallId,
      messageId: item.part.messageId,
      snapshot,
      tool: item.part.tool,
    });
    if (!seenPaths.has(snapshot.path)) {
      seenPaths.add(snapshot.path);
      anchorPartIds.add(item.part.id);
      anchorMessageIdByPartId.set(item.part.id, item.part.messageId);
    }
  }
  closeSegment();
  return segments;
}

/** Project raw activity rows into scoped spans and turn-folded diff rows. */
export function projectActivitySpans(
  items: readonly RawTranscriptItem[],
  opts?: ActivitySpanProjectionOptions,
): DisplayTranscriptItem[] {
  const segments = collectSegmentFolds(items);
  let segmentIndex = 0;
  let segment = segments[0];

  const out: DisplayTranscriptItem[] = [];
  let scope: string | null = null;
  let run: ActivityCandidate[] = [];
  /** First writes in this run that anchor a segment's diff cards. */
  let runAnchorPartIds: string[] = [];
  let workerRun: Extract<RawTranscriptItem, { kind: "tool" }>[] = [];

  const emitAnchoredFolds = () => {
    if (runAnchorPartIds.length === 0 || !segment) return;
    const anchored = new Set(runAnchorPartIds);
    const folds = segment.folds.filter((fold) => anchored.has(fold.key));
    if (folds.length === 0) return;
    const first = folds[0]!;
    out.push({
      kind: "file_edit",
      key: `file-edit:${first.key}`,
      folds,
      anchorMessageId:
        segment.anchorMessageIdByPartId.get(first.key) ?? first.key,
    });
  };

  const flushRun = () => {
    if (run.length > 0 && runHasWork(run)) out.push(emitActivitySpan(run));
    emitAnchoredFolds();
    scope = null;
    run = [];
    runAnchorPartIds = [];
  };

  const flushWorkerRun = () => {
    out.push(...emitWorkerRun(workerRun));
    workerRun = [];
  };

  for (const item of items) {
    if (item.kind === "tool" && isTaskToolName(item.part.tool)) {
      flushRun();
      workerRun.push(item);
      continue;
    }
    flushWorkerRun();
    if (item.kind !== "tool" && item.kind !== "index_warming" && item.kind !== "turn_load") {
      flushRun();
      // Flush pending folds before the next user segment.
      if (item.kind === "user") {
        segmentIndex += 1;
        segment = segments[segmentIndex];
      }
      out.push(item);
      continue;
    }
    const candidate = activityCandidate(item, opts);
    // A first-touch write keeps later calls below its diff card.
    if (
      scope !== null &&
      (scope !== candidate.scope ||
        (runAnchorPartIds.length > 0 && !candidateIsFileEdit(candidate)))
    ) {
      flushRun();
    }
    scope = candidate.scope;
    run.push(candidate);
    if (
      candidate.entry.kind === "tool" &&
      segment?.anchorPartIds.has(candidate.entry.part.id)
    ) {
      runAnchorPartIds.push(candidate.entry.part.id);
    }
  }
  flushRun();
  flushWorkerRun();
  return out;
}

function entryHintTitle(entry: ActivitySpanEntry): string {
  if (entry.kind === "index_warming") {
    return indexWarmingChickletTitle(entry.meta);
  }
  if (entry.kind === "turn_load") return turnLoadRowSummary(entry.row).title;
  return toolPartSummaryTitle(entry.part);
}

/** Collapsed context lists failures first. */
export function activitySpanHint(
  entries: readonly ActivitySpanEntry[],
  label: string,
  sessionId?: string,
): string {
  const failed = (entry: ActivitySpanEntry): boolean =>
    entry.kind === "tool" &&
    toolPartDisplayStatus(entry.part, sessionId) === "error";
  const ordered = [...entries].sort(
    (a, b) => Number(failed(b)) - Number(failed(a)),
  );
  const unique = [
    ...new Set(
      ordered
        .map((entry) => entryHintTitle(entry))
        .filter((title) => title && title !== label),
    ),
  ];
  const [first, ...rest] = unique;
  if (!first) return "";
  if (rest.length === 0) return first;
  return `${first}, +${rest.length}`;
}
