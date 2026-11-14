import { fileEditsFromPart } from "./file-edit-model.ts";
import type { FileEditPreview } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";
import type { RawTranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import { sourceChangeFromPresence, type SourceReaderChange } from "../../components/source/reader/source-reader-change.ts";

/** One write in run order. */
export type FileEditStep = {
  /** Transcript row identity. */
  key: string;
  toolCallId?: string;
  messageId?: string;
  snapshot: FileEditPreview;
  tool: string;
  ts?: string;
};

export type FileEditLineStat = {
  added: number;
  removed: number;
};

/** Whole-file writes to one path and their composed diff. */
export type FileEditFold = {
  path: string;
  /** First-write identity keeps the card mounted. */
  key: string;
  /** First pre-image → last post-image. */
  net: Omit<FileEditPreview, "added" | "removed"> & { added: number | null; removed: number | null };
  steps: FileEditStep[];
};

/** A file's identity within a turn: the same path in two roots is two files. */
export function fileEditPathKey(snapshot: Pick<FileEditPreview, "root_id" | "path">): string {
  return snapshot.root_id ? `${snapshot.root_id}\0${snapshot.path}` : snapshot.path;
}

/** Group writes by path, first touch first, and compose each path's range. */
export function foldFileEdits(
  steps: readonly FileEditStep[],
): FileEditFold[] {
  const order: string[] = [];
  const byPath = new Map<string, FileEditStep[]>();
  for (const step of steps) {
    const path = fileEditPathKey(step.snapshot);
    const existing = byPath.get(path);
    if (existing) {
      existing.push(step);
      continue;
    }
    order.push(path);
    byPath.set(path, [step]);
  }
  return order.map((path) => {
    const pathSteps = byPath.get(path) ?? [];
    return foldFromSteps(pathSteps[0]!.snapshot.path, pathSteps);
  });
}

function foldFromSteps(
  path: string,
  steps: readonly FileEditStep[],
): FileEditFold {
  const first = steps[0]!;
  const last = steps[steps.length - 1]!;
  return {
    path,
    key: first.key,
    net: {
      ...last.snapshot,
      path,
      before_sha256: first.snapshot.before_sha256,
      created: first.snapshot.created,
      added: steps.length === 1 ? last.snapshot.added : null,
      removed: steps.length === 1 ? last.snapshot.removed : null,
    },
    steps: [...steps],
  };
}

/** Hash input covering the full write chain. */
export function fileEditFoldFingerprint(fold: FileEditFold): string {
  const parts = [fold.net.root_id ?? "", String(fold.net.deleted ?? false), fold.path, fold.net.before_sha256];
  for (const step of fold.steps) {
    parts.push(step.key, step.toolCallId ?? "", step.messageId ?? "", step.snapshot.after_sha256);
  }
  return parts.join("\0");
}

export function singleFileEditFold(
  key: string,
  snapshot: FileEditPreview,
  tool = "write",
): FileEditFold {
  return foldFromSteps(snapshot.path, [{ key, snapshot, tool }]);
}

/** Preserve call order when collecting writes. */
export function fileEditStepsFromParts(
  parts: readonly ToolPartView[],
): FileEditStep[] {
  return parts.flatMap(fileEditStepsFromPart);
}

/** One step per file the call edited; a call that edits several files anchors several folds. */
export function fileEditStepsFromPart(part: ToolPartView): FileEditStep[] {
  return fileEditsFromPart(part).map((snapshot, index) => ({
    key: `${part.id}:${index}`,
    toolCallId: part.toolCallId,
    messageId: part.messageId,
    snapshot,
    tool: part.tool,
  }));
}

/** One turn's writes folded by file. */
export type SegmentFolds = {
  folds: FileEditFold[];
  /** The folds each call anchors: the files it was first to write this turn. */
  anchorFoldKeysByPartId: Map<string, string[]>;
  /** Changes each call made past its result's bound. */
  omittedByPartId: Map<string, number>;
};

/** Repeated writes to a file share the card anchored at its first write. */
export function collectSegmentFolds(
  items: readonly RawTranscriptItem[],
): SegmentFolds[] {
  const segments: SegmentFolds[] = [];
  let steps: FileEditStep[] = [];
  let anchorFoldKeysByPartId = new Map<string, string[]>();
  let omittedByPartId = new Map<string, number>();
  let seenFiles = new Set<string>();

  const closeSegment = () => {
    segments.push({ folds: foldFileEdits(steps), anchorFoldKeysByPartId, omittedByPartId });
    steps = [];
    anchorFoldKeysByPartId = new Map();
    omittedByPartId = new Map();
    seenFiles = new Set();
  };

  for (const item of items) {
    if (item.kind === "user") {
      closeSegment();
      continue;
    }
    if (item.kind !== "tool") continue;
    const omitted = item.part.fileEditsOmitted ?? 0;
    if (omitted > 0) omittedByPartId.set(item.part.id, omitted);
    for (const step of fileEditStepsFromPart(item.part)) {
      steps.push(step);
      const file = fileEditPathKey(step.snapshot);
      if (seenFiles.has(file)) continue;
      seenFiles.add(file);
      const anchored = anchorFoldKeysByPartId.get(item.part.id);
      if (anchored) anchored.push(step.key);
      else anchorFoldKeysByPartId.set(item.part.id, [step.key]);
    }
  }
  closeSegment();
  return segments;
}

export function fileEditFoldIsNoop(fold: FileEditFold): boolean {
  return fold.net.before_sha256 === fold.net.after_sha256 && fold.net.created === fold.net.deleted;
}

/** A net range or single write, read from the edit's own presence facts before any reader loads. */
export function fileEditChange(snapshot: { created: boolean; deleted?: boolean }): SourceReaderChange {
  return sourceChangeFromPresence(!snapshot.created, !snapshot.deleted);
}

/** Counts summaries already supplied by the host. A composed range waits for its own summary. */
export function fileEditLineStat(snapshot: { added: number | null; removed: number | null }): FileEditLineStat | null {
  return snapshot.added == null || snapshot.removed == null ? null : { added: snapshot.added, removed: snapshot.removed };
}
