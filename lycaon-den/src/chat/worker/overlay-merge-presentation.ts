import type { WorkerMergeResult, WorkerReadyResolution } from "../../api/types.ts";
import {
  type StructuredToolSection,
  type ToolFact,
} from "../tool/tool-presentation-contract.ts";

function readyResolutionLine(r: WorkerReadyResolution): string {
  const flags = [
    r.path_class,
    r.conflict_tier,
    r.action,
    r.needs_review ? "review" : "auto",
  ]
    .filter(Boolean)
    .join(" · ");
  const advisory = r.advisory?.trim();
  return advisory ? `${r.path} (${flags}) — ${advisory}` : `${r.path} (${flags})`;
}

/** Compact merge-plan presentation for preview_overlay / promote_overlay coordinator cards. */
export function overlayMergePresentationSections(
  result: Record<string, unknown>,
): StructuredToolSection[] {
  const parsed = result as WorkerMergeResult;
  const facts: ToolFact[] = [];
  if (parsed.merge_status) {
    facts.push({ label: "Merge status", value: parsed.merge_status });
  }
  if (parsed.clean_paths?.length) {
    facts.push({
      label: "Clean paths",
      value: String(parsed.clean_paths.length),
    });
  }
  if (parsed.applied?.length) {
    facts.push({ label: "Applied", value: String(parsed.applied.length) });
  }
  const sections: StructuredToolSection[] = [];
  if (facts.length) {
    sections.push({ kind: "facts", facts });
  }
  if (parsed.applied?.length) {
    sections.push({
      kind: "content",
      text: parsed.applied.join("\n"),
      label: "Applied paths",
    });
  }
  if (parsed.clean_paths?.length) {
    sections.push({
      kind: "content",
      text: parsed.clean_paths.join("\n"),
      label: "Clean paths",
    });
  }
  const ready = parsed.ready_resolutions ?? [];
  if (ready.length) {
    const auto = ready.filter((r) => !r.needs_review).length;
    const review = ready.length - auto;
    sections.push({
      kind: "note",
      text: `Ready resolutions: ${auto} auto, ${review} need review`,
    });
    sections.push({
      kind: "content",
      text: ready.map(readyResolutionLine).join("\n"),
    });
  }
  if (!ready.length && parsed.path_status?.length) {
    sections.push({
      kind: "content",
      text: parsed.path_status
        .map((row) => `${row.path} — ${row.status}${row.conflict_tier ? ` (${row.conflict_tier})` : ""}`)
        .join("\n"),
    });
  }
  return sections;
}
