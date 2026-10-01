import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import type {

  SourceReaderEndpoint,
  SourceWalkEffect,
} from "../../api/types.ts";
import { countLabel } from "./review-model.ts";
import {
  comparisonEndpoints,
} from "../source/source-comparison.ts";
import { walkStepEffects, type WalkStep } from "../walk/walk-model.ts";

type ReviewWalkChangeArea = {
  label: string;
  added: number;
  removed: number;
};

export type ReviewWalkDetails = {
  summary: string;
  added: number;
  removed: number;
  beforeLines: number | null;
  afterLines: number | null;
  areas: ReviewWalkChangeArea[];
  remainingAreas: number;
  availabilityNote: string | null;
};

type ReviewWalkFileProgress = {
  position: number;
  count: number;
};

function readable(side: SourceReaderEndpoint): boolean {
  return side.availability === "available" || side.availability === "absent";
}

function areaLocation(start: number, end: number): string {
  const count = end - start;
  if (count <= 0) return `At line ${start + 1}`;
  if (count === 1) return `Line ${start + 1}`;
  return `Lines ${start + 1}–${end}`;
}

function availabilityNote(endpoints: { before: SourceReaderEndpoint; after: SourceReaderEndpoint }): string | null {
  const availability = [
    endpoints.before.availability,
    endpoints.after.availability,
  ];
  if (availability.includes("binary")) return "This step changes a binary file.";
  if (availability.includes("directory")) return "This step changes a directory.";
  if (availability.some((value) => value !== "available" && value !== "absent")) {
    return "Line-level details are unavailable for this step.";
  }
  return null;
}

function summary(
  effect: SourceWalkEffect,
  added: number,
  removed: number,
  areaCount: number,
  beforeLines: number | null,
  afterLines: number | null,
): string {
  switch (effect.op) {
    case "create":
      return afterLines === null
        ? "Created this file."
        : `Created a file with ${countLabel(afterLines, "line")}.`;
    case "delete":
      return beforeLines === null
        ? "Deleted this file."
        : `Deleted a file with ${countLabel(beforeLines, "line")}.`;
    case "rename":
      if (added === 0 && removed === 0) {
        return "Renamed this file without changing its contents.";
      }
      break;
    case "write":
      break;
  }
  if (added === 0 && removed === 0) {
    return "Recorded a file update with no line changes.";
  }
  const impact = added > 0 && removed > 0
    ? `Added ${countLabel(added, "line")} and removed ${countLabel(removed, "line")}`
    : added > 0
    ? `Added ${countLabel(added, "line")}`
    : `Removed ${countLabel(removed, "line")}`;
  return `${impact} across ${countLabel(areaCount, "area")}.`;
}

function basicSummary(effect: SourceWalkEffect): string {
  switch (effect.op) {
    case "create":
      return "Created this file.";
    case "delete":
      return "Deleted this file.";
    case "rename":
      return "Renamed this file.";
    case "write":
      return "Modified this file.";
  }
}

export function reviewWalkFileProgress(
  current: WalkStep,
  steps: readonly WalkStep[],
): ReviewWalkFileProgress | null {
  // A group step spans files, so per-file progression has no single answer.
  if (current.kind !== "effect") return null;
  const fileId = current.effect.file_id.trim();
  if (!fileId) return null;
  const matching = steps.filter((step) =>
    walkStepEffects(step).some((effect) => effect.file_id.trim() === fileId)
  );
  const at = matching.findIndex((step) => step.key === current.key);
  if (at < 0) return null;
  return {
    position: at + 1,
    count: matching.length,
  };
}

export function buildReviewWalkDetails(
  effect: SourceWalkEffect,
  comparison: ComparisonSnapshot | null,
): ReviewWalkDetails {
  const endpoints = comparisonEndpoints(comparison);
  const reader = comparison?.summary;
  if (!endpoints || !reader || !readable(endpoints.before) || !readable(endpoints.after)) {
    return {
      summary: basicSummary(effect),
      added: 0,
      removed: 0,
      beforeLines: null,
      afterLines: null,
      areas: [],
      remainingAreas: 0,
      availabilityNote: endpoints ? availabilityNote(endpoints) : null,
    };
  }

  const { added, removed } = reader;
  const areas = reader.change_areas.map(area => ({
    label: areaLocation(area.after_line - 1, area.after_line - 1 + area.added),
    added: area.added,
    removed: area.removed,
  }));
  const beforeLines = reader.before.lines;
  const afterLines = reader.after.lines;
  return {
    summary: summary(
      effect,
      added,
      removed,
      reader.change_area_count,
      beforeLines,
      afterLines,
    ),
    added,
    removed,
    beforeLines,
    afterLines,
    areas,
    remainingAreas: Math.max(0, reader.change_area_count - areas.length),
    availabilityNote: null,
  };
}
